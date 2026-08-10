#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"
require "optparse"

options = {}
OptionParser.new do |parser|
  parser.on("--previous DIR") { |value| options[:previous] = value }
  parser.on("--current DIR") { |value| options[:current] = value }
end.parse!
abort "usage: verify-crd-compatibility.rb --previous DIR --current DIR" unless options.values_at(:previous, :current).all?

def load_crds(directory)
  Dir.glob(File.join(directory, "*.{yaml,yml}")).sort.flat_map do |path|
    YAML.load_stream(File.read(path)).compact
  end.to_h { |crd| [crd.dig("metadata", "name"), crd] }
end

def compatible_schema!(previous, current, path)
  %w[type format x-kubernetes-int-or-string x-kubernetes-preserve-unknown-fields].each do |field|
    next unless previous.key?(field)
    abort "#{path}: #{field} changed" unless previous[field] == current[field]
  end

  if current["enum"]
    abort "#{path}: enum constraint was added" unless previous["enum"]
    removed = previous["enum"] - current["enum"]
    abort "#{path}: enum values removed: #{removed.inspect}" unless removed.empty?
  end

  %w[minimum minLength minItems minProperties].each do |field|
    next unless current.key?(field)
    abort "#{path}: #{field} constraint was added" unless previous.key?(field)
    abort "#{path}: #{field} became more restrictive" if current[field] > previous[field]
  end
  %w[maximum maxLength maxItems maxProperties].each do |field|
    next unless current.key?(field)
    abort "#{path}: #{field} constraint was added" unless previous.key?(field)
    abort "#{path}: #{field} became more restrictive" if current[field] < previous[field]
  end
  %w[pattern uniqueItems x-kubernetes-validations x-kubernetes-list-type
     x-kubernetes-list-map-keys oneOf anyOf allOf not].each do |field|
    next unless current.key?(field)
    abort "#{path}: #{field} constraint was added or changed" unless previous[field] == current[field]
  end

  previous_required = Array(previous["required"])
  current_required = Array(current["required"])
  added_required = current_required - previous_required
  abort "#{path}: new required fields: #{added_required.join(', ')}" unless added_required.empty?

  previous_properties = previous.fetch("properties", {})
  current_properties = current.fetch("properties", {})
  previous_properties.each do |name, schema|
    abort "#{path}: property #{name} was removed" unless current_properties.key?(name)
    compatible_schema!(schema, current_properties.fetch(name), "#{path}.#{name}")
  end

  if previous["items"]
    abort "#{path}: array items schema was removed" unless current["items"]
    compatible_schema!(previous["items"], current["items"], "#{path}[]")
  end
end

previous_crds = load_crds(options.fetch(:previous))
current_crds = load_crds(options.fetch(:current))
abort "previous release contains no CRDs" if previous_crds.empty?
added_crds = current_crds.keys - previous_crds.keys
abort "new CRDs are not rollback-safe for the public alpha: #{added_crds.join(', ')}" unless added_crds.empty?

previous_crds.each do |name, previous|
  current = current_crds[name] or abort "CRD #{name} was removed"
  %w[group names scope].each do |field|
    abort "CRD #{name} changed spec.#{field}" unless previous.dig("spec", field) == current.dig("spec", field)
  end

  current_versions = current.dig("spec", "versions").to_a.to_h { |version| [version["name"], version] }
  previous.dig("spec", "versions").to_a.each do |version|
    next unless version["served"] || version["storage"]
    current_version = current_versions[version["name"]] or abort "CRD #{name} removed version #{version['name']}"
    abort "CRD #{name} no longer serves #{version['name']}" unless current_version["served"]
    compatible_schema!(version.dig("schema", "openAPIV3Schema") || {},
                       current_version.dig("schema", "openAPIV3Schema") || {},
                       "#{name}/#{version['name']}")
  end
end

puts "current CRDs are backward-compatible with the recorded rollback release"

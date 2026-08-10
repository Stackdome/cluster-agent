#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

expected_path, actual_path = ARGV
abort "usage: verify-installed-crds.rb <expected-bundle.yaml> <kubectl-output.yaml>" unless expected_path && actual_path

expected = YAML.load_stream(File.read(expected_path)).compact.to_h do |crd|
  [crd.dig("metadata", "name"), crd.fetch("spec")]
end
actual_document = YAML.safe_load(File.read(actual_path), aliases: true)
actual_items = actual_document["kind"] == "List" ? actual_document.fetch("items") : [actual_document]
actual = actual_items.to_h { |crd| [crd.dig("metadata", "name"), crd.fetch("spec")] }

abort "installed CRD names differ from rollback bundle" unless actual.keys.sort == expected.keys.sort

def require_expected_fields!(expected, actual, path)
  if expected.is_a?(Hash)
    abort "#{path} is not an object" unless actual.is_a?(Hash)
    expected.each do |key, value|
      abort "#{path}.#{key} is missing" unless actual.key?(key)
      require_expected_fields!(value, actual.fetch(key), "#{path}.#{key}")
    end
  elsif expected.is_a?(Array)
    abort "#{path} differs from rollback bundle" unless actual == expected
  else
    abort "#{path} differs from rollback bundle" unless actual == expected
  end
end

expected.each do |name, spec|
  require_expected_fields!(spec, actual.fetch(name), name)
end

puts "installed CRD specs match the recorded rollback bundle"

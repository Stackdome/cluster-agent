#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "optparse"
require "pathname"

options = {}
OptionParser.new do |parser|
  parser.on("--artifact-dir DIR") { |value| options[:artifact_dir] = value }
  parser.on("--agent-ref REF") { |value| options[:agent_ref] = value }
  parser.on("--reconciler-ref REF") { |value| options[:reconciler_ref] = value }
end.parse!

required = %i[artifact_dir agent_ref reconciler_ref]
missing = required.reject { |key| options[key] }
abort "missing options: #{missing.join(', ')}" unless missing.empty?

digest_reference = /\A[^@\s]+@sha256:[0-9a-f]{64}\z/
%i[agent_ref reconciler_ref].each do |key|
  abort "#{key} is not an immutable image reference" unless options.fetch(key).match?(digest_reference)
end

artifact_dir = Pathname.new(options.fetch(:artifact_dir))
expected_files = []

def load_json(path)
  JSON.parse(path.read)
rescue JSON::ParserError => error
  abort "#{path}: invalid JSON: #{error.message}"
end

def require_equal(path, field, actual, expected)
  return if actual == expected

  abort "#{path}: #{field} was #{actual.inspect}, expected #{expected.inspect}"
end

{"agent" => options.fetch(:agent_ref), "reconciler" => options.fetch(:reconciler_ref)}.each do |name, image_ref|
  %w[amd64 arm64].each do |arch|
    platform = "linux/#{arch}"
    trivy_path = artifact_dir.join("trivy-#{name}-linux-#{arch}.json")
    spdx_path = artifact_dir.join("#{name}-linux-#{arch}.spdx.json")
    cyclonedx_path = artifact_dir.join("#{name}-linux-#{arch}.cyclonedx.json")
    expected_files.concat([trivy_path, spdx_path, cyclonedx_path])

    trivy = load_json(trivy_path)
    require_equal(trivy_path, "SchemaVersion", trivy["SchemaVersion"], 2)
    require_equal(trivy_path, "ArtifactName", trivy["ArtifactName"], image_ref)
    require_equal(trivy_path, "Results type", trivy["Results"].class, Array)
    require_equal(trivy_path, "RepoDigests", trivy.dig("Metadata", "RepoDigests"), [image_ref])
    require_equal(trivy_path, "OS", trivy.dig("Metadata", "ImageConfig", "os"), "linux")
    require_equal(trivy_path, "architecture", trivy.dig("Metadata", "ImageConfig", "architecture"), arch)

    spdx = load_json(spdx_path)
    abort "#{spdx_path}: invalid SPDX version" unless spdx["spdxVersion"].to_s.start_with?("SPDX-")
    comments = Array(spdx["annotations"]).map { |annotation| annotation["comment"] }
    require_equal(spdx_path, "image binding", comments.count("stackdome:image-reference=#{image_ref}"), 1)
    require_equal(spdx_path, "platform binding", comments.count("stackdome:platform=#{platform}"), 1)

    cyclonedx = load_json(cyclonedx_path)
    require_equal(cyclonedx_path, "bomFormat", cyclonedx["bomFormat"], "CycloneDX")
    properties = Array(cyclonedx.dig("metadata", "component", "properties"))
      .group_by { |property| property["name"] }
      .transform_values { |items| items.map { |item| item["value"] } }
    require_equal(cyclonedx_path, "image binding", properties["stackdome:image-reference"], [image_ref])
    require_equal(cyclonedx_path, "platform binding", properties["stackdome:platform"], [platform])
  end
end

actual_files = artifact_dir.children.select(&:file?).select do |path|
  path.basename.to_s.match?(/(?:\.spdx\.json|\.cyclonedx\.json|trivy-.*\.json)\z/)
end
require_equal(artifact_dir, "evidence filenames", actual_files.map(&:basename).map(&:to_s).sort,
              expected_files.map(&:basename).map(&:to_s).sort)

puts "release evidence is bound to the expected digests and platforms"

#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "optparse"
require "pathname"
require "uri"

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

def oci_purl_identity(path, purl)
  match = purl.to_s.match(%r{\Apkg:oci/([^@?]+)(?:@[^?]+)?\?(.+)\z})
  abort "#{path}: invalid OCI purl #{purl.inspect}" unless match

  query = URI.decode_www_form(match[2])
  architectures = query.each_with_object([]) do |(key, value), values|
    values << value if key == "arch"
  end
  abort "#{path}: OCI purl must contain exactly one architecture" unless architectures.length == 1

  [URI.decode_www_form_component(match[1]), architectures.first]
end

{"agent" => options.fetch(:agent_ref), "reconciler" => options.fetch(:reconciler_ref)}.each do |name, image_ref|
  repository, digest = image_ref.split("@", 2)
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
    described_ids = Array(spdx["relationships"]).each_with_object([]) do |relationship, ids|
      next unless relationship["spdxElementId"] == "SPDXRef-DOCUMENT"
      next unless relationship["relationshipType"] == "DESCRIBES"

      ids << relationship["relatedSpdxElement"]
    end
    described_ids = Array(spdx["documentDescribes"]) if described_ids.empty?
    require_equal(spdx_path, "described package count", described_ids.length, 1)
    described_packages = Array(spdx["packages"]).select { |package| package["SPDXID"] == described_ids.first }
    require_equal(spdx_path, "described package count", described_packages.length, 1)
    described_package = described_packages.first
    require_equal(spdx_path, "described container purpose", described_package["primaryPackagePurpose"], "CONTAINER")
    require_equal(spdx_path, "described container name", described_package["name"], repository)
    require_equal(spdx_path, "described container version", described_package["versionInfo"], digest)
    spdx_purls = Array(described_package["externalRefs"]).each_with_object([]) do |reference, purls|
      next unless reference["referenceType"] == "purl"
      next unless reference["referenceLocator"].to_s.start_with?("pkg:oci/")

      purls << reference["referenceLocator"]
    end
    require_equal(spdx_path, "described container OCI purl count", spdx_purls.length, 1)
    require_equal(spdx_path, "described container OCI purl identity",
                  oci_purl_identity(spdx_path, spdx_purls.first), [repository, arch])
    comments = Array(spdx["annotations"]).map { |annotation| annotation["comment"] }
    require_equal(spdx_path, "image binding", comments.count("stackdome:image-reference=#{image_ref}"), 1)
    require_equal(spdx_path, "platform binding", comments.count("stackdome:platform=#{platform}"), 1)

    cyclonedx = load_json(cyclonedx_path)
    require_equal(cyclonedx_path, "bomFormat", cyclonedx["bomFormat"], "CycloneDX")
    component = cyclonedx.dig("metadata", "component") || {}
    require_equal(cyclonedx_path, "root component type", component["type"], "container")
    require_equal(cyclonedx_path, "root component name", component["name"], repository)
    require_equal(cyclonedx_path, "root component version", component["version"], digest)
    require_equal(cyclonedx_path, "root component OCI purl identity",
                  oci_purl_identity(cyclonedx_path, component["purl"]), [repository, arch])
    properties = Array(component["properties"])
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

#!/usr/bin/env ruby
# frozen_string_literal: true

require "digest"
require "json"
require "optparse"
require "pathname"
require "uri"

DIGEST_PATTERN = /\Asha256:[0-9a-f]{64}\z/
INDEX_MEDIA_TYPES = [
  "application/vnd.oci.image.index.v1+json",
  "application/vnd.docker.distribution.manifest.list.v2+json",
].freeze
MANIFEST_MEDIA_TYPES = [
  "application/vnd.oci.image.manifest.v1+json",
  "application/vnd.docker.distribution.manifest.v2+json",
].freeze

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

def platform_manifests(index_path, expected_index_digest)
  actual_digest = "sha256:#{Digest::SHA256.file(index_path).hexdigest}"
  require_equal(index_path, "raw index digest", actual_digest, expected_index_digest)
  index = load_json(index_path)
  require_equal(index_path, "schemaVersion", index["schemaVersion"], 2)
  abort "#{index_path}: document is not an OCI image index" unless INDEX_MEDIA_TYPES.include?(index["mediaType"])

  descriptors = Array(index["manifests"])
  platforms = descriptors.map do |manifest|
    platform = manifest["platform"] || {}
    "#{platform['os']}/#{platform['architecture']}"
  end
  expected = %w[linux/amd64 linux/arm64]
  require_equal(index_path, "platform manifests", platforms.sort, expected.sort)

  manifests = descriptors.each_with_object({}) do |manifest, result|
    platform = manifest["platform"] || {}
    name = "#{platform['os']}/#{platform['architecture']}"
    abort "#{index_path}: duplicate #{name} manifest" if result.key?(name)
    digest = manifest["digest"].to_s
    abort "#{index_path}: #{name} manifest lacks a SHA-256 digest" unless digest.match?(DIGEST_PATTERN)
    unless MANIFEST_MEDIA_TYPES.include?(manifest["mediaType"])
      abort "#{index_path}: #{name} manifest has an invalid media type"
    end
    result[name] = {"digest" => digest, "mediaType" => manifest["mediaType"]}
  end
  manifests
end

def oci_purl_identity(path, purl)
  match = purl.to_s.match(%r{\Apkg:oci/([^@?]+)@([^?]+)\?(.+)\z})
  abort "#{path}: invalid OCI purl #{purl.inspect}" unless match

  repository = URI.decode_www_form_component(match[1])
  manifest_digest = URI.decode_www_form_component(match[2])
  abort "#{path}: OCI purl version is not a SHA-256 digest" unless manifest_digest.match?(DIGEST_PATTERN)
  query = URI.decode_www_form(match[3])
  abort "#{path}: OCI purl must contain only one architecture qualifier" unless query.length == 1 && query[0][0] == "arch"

  [repository, manifest_digest, query[0][1]]
end

{"agent" => options.fetch(:agent_ref), "reconciler" => options.fetch(:reconciler_ref)}.each do |name, image_ref|
  repository, index_digest = image_ref.split("@", 2)
  index_path = artifact_dir.join("#{name}-index.json")
  expected_files << index_path
  manifests = platform_manifests(index_path, index_digest)

  %w[amd64 arm64].each do |arch|
    platform = "linux/#{arch}"
    platform_manifest = manifests.fetch(platform)
    trivy_path = artifact_dir.join("trivy-#{name}-linux-#{arch}.json")
    spdx_path = artifact_dir.join("#{name}-linux-#{arch}.spdx.json")
    cyclonedx_path = artifact_dir.join("#{name}-linux-#{arch}.cyclonedx.json")
    syft_path = artifact_dir.join("#{name}-linux-#{arch}.syft.json")
    expected_files.concat([trivy_path, spdx_path, cyclonedx_path, syft_path])

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
    require_equal(spdx_path, "described container index digest", described_package["versionInfo"], index_digest)
    spdx_purls = Array(described_package["externalRefs"]).each_with_object([]) do |reference, purls|
      next unless reference["referenceType"] == "purl"
      next unless reference["referenceLocator"].to_s.start_with?("pkg:oci/")

      purls << reference["referenceLocator"]
    end
    require_equal(spdx_path, "described container OCI purl count", spdx_purls.length, 1)
    require_equal(spdx_path, "scanner-native OCI purl identity",
                  oci_purl_identity(spdx_path, spdx_purls.first),
                  [repository, platform_manifest.fetch("digest"), arch])
    injected_spdx_claims = Array(spdx["annotations"]).map { |annotation| annotation["comment"] }
                                                       .compact
                                                       .grep(/\Astackdome:/)
    require_equal(spdx_path, "repository-injected scanner identity claims", injected_spdx_claims, [])

    cyclonedx = load_json(cyclonedx_path)
    require_equal(cyclonedx_path, "bomFormat", cyclonedx["bomFormat"], "CycloneDX")
    require_equal(cyclonedx_path, "specVersion", cyclonedx["specVersion"], "1.6")
    component = cyclonedx.dig("metadata", "component") || {}
    require_equal(cyclonedx_path, "root component type", component["type"], "container")
    require_equal(cyclonedx_path, "root component name", component["name"], repository)
    require_equal(cyclonedx_path, "root component index digest", component["version"], index_digest)
    require_equal(cyclonedx_path, "repository-injected root purl", component["purl"], nil)
    injected_properties = Array(component["properties"]).select do |property|
      property["name"].to_s.start_with?("stackdome:")
    end
    require_equal(cyclonedx_path, "repository-injected scanner identity properties", injected_properties, [])

    syft = load_json(syft_path)
    require_equal(syft_path, "Syft schema", syft.dig("schema", "version"), "16.0.39")
    require_equal(syft_path, "scanner name", syft.dig("descriptor", "name"), "syft")
    require_equal(syft_path, "scanner version", syft.dig("descriptor", "version"), "1.32.0")
    require_equal(syft_path, "source type", syft.dig("source", "type"), "image")
    require_equal(syft_path, "source repository", syft.dig("source", "name"), repository)
    require_equal(syft_path, "source index digest", syft.dig("source", "version"), index_digest)
    require_equal(syft_path, "source input", syft.dig("source", "metadata", "userInput"), image_ref)
    require_equal(syft_path, "source manifest digest",
                  syft.dig("source", "metadata", "manifestDigest"), platform_manifest.fetch("digest"))
    require_equal(syft_path, "source ID",
                  syft.dig("source", "id"), platform_manifest.fetch("digest").delete_prefix("sha256:"))
    require_equal(syft_path, "source manifest media type",
                  syft.dig("source", "metadata", "mediaType"), platform_manifest.fetch("mediaType"))
    require_equal(syft_path, "source OS", syft.dig("source", "metadata", "os"), "linux")
    require_equal(syft_path, "source architecture", syft.dig("source", "metadata", "architecture"), arch)
  end
end

actual_files = artifact_dir.children.select(&:file?).select do |path|
  path.basename.to_s.match?(%r{\A(?:agent|reconciler)-(?:index|linux-(?:amd64|arm64)\.(?:spdx|cyclonedx|syft))\.json\z}) ||
    path.basename.to_s.match?(%r{\Atrivy-(?:agent|reconciler)-linux-(?:amd64|arm64)\.json\z})
end
require_equal(artifact_dir, "evidence filenames", actual_files.map(&:basename).map(&:to_s).sort,
              expected_files.map(&:basename).map(&:to_s).sort)

puts "release evidence is bound to scanner-native index and platform identities"

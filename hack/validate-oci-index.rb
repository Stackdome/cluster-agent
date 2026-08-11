#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "digest"

path = ARGV.fetch(0) { abort "usage: validate-oci-index.rb <index-manifest.json|oci-layout-directory>" }
layout = File.directory?(path) ? path : nil
document_path = layout ? File.join(layout, "index.json") : path
index = JSON.parse(File.read(document_path))
media_types = ["application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json"]
manifest_media_types = ["application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"]
digest_pattern = /\Asha256:[0-9a-f]{64}\z/
abort "#{path}: schemaVersion must be 2" unless index["schemaVersion"] == 2
abort "#{path}: document is not an OCI image index" unless media_types.include?(index["mediaType"])

manifests = Array(index["manifests"])
if layout && manifests.none? { |manifest| manifest.dig("platform", "os") }
  nested = manifests.select { |manifest| media_types.include?(manifest["mediaType"]) }
  abort "#{path}: OCI layout does not point to exactly one image index" unless nested.length == 1
  algorithm, digest = nested.first.fetch("digest").split(":", 2)
  abort "#{path}: unsupported OCI digest algorithm" unless algorithm == "sha256"
  nested_path = File.join(layout, "blobs", algorithm, digest)
  actual_digest = Digest::SHA256.file(nested_path).hexdigest
  abort "#{path}: nested index digest does not match its descriptor" unless actual_digest == digest
  index = JSON.parse(File.read(nested_path))
  abort "#{path}: nested schemaVersion must be 2" unless index["schemaVersion"] == 2
  abort "#{path}: nested document is not an OCI image index" unless media_types.include?(index["mediaType"])
  manifests = Array(index["manifests"])
end

platforms = manifests.map do |manifest|
  abort "#{path}: platform manifest has an invalid media type" unless manifest_media_types.include?(manifest["mediaType"])
  abort "#{path}: platform manifest lacks a SHA-256 digest" unless manifest["digest"].to_s.match?(digest_pattern)
  platform = manifest["platform"] || {}
  "#{platform['os']}/#{platform['architecture']}"
end
expected = %w[linux/amd64 linux/arm64]
abort "#{path}: platforms #{platforms.inspect}, expected exactly #{expected.inspect}" unless platforms.sort == expected.sort

puts "#{path}: exact linux/amd64 and linux/arm64 image index verified"

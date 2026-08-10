#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"

path = ARGV.fetch(0) { abort "usage: validate-oci-index.rb <index-manifest.json|oci-layout-directory>" }
layout = File.directory?(path) ? path : nil
document_path = layout ? File.join(layout, "index.json") : path
index = JSON.parse(File.read(document_path))
media_types = ["application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json"]
abort "#{path}: document is not an OCI image index" unless media_types.include?(index["mediaType"])

manifests = Array(index["manifests"])
if layout && manifests.none? { |manifest| manifest.dig("platform", "os") }
  nested = manifests.select { |manifest| media_types.include?(manifest["mediaType"]) }
  abort "#{path}: OCI layout does not point to exactly one image index" unless nested.length == 1
  algorithm, digest = nested.first.fetch("digest").split(":", 2)
  abort "#{path}: unsupported OCI digest algorithm" unless algorithm == "sha256"
  index = JSON.parse(File.read(File.join(layout, "blobs", algorithm, digest)))
  manifests = Array(index["manifests"])
end

platforms = manifests.map do |manifest|
  platform = manifest["platform"] || {}
  "#{platform['os']}/#{platform['architecture']}"
end
expected = %w[linux/amd64 linux/arm64]
abort "#{path}: platforms #{platforms.inspect}, expected exactly #{expected.inspect}" unless platforms.sort == expected.sort

puts "#{path}: exact linux/amd64 and linux/arm64 image index verified"

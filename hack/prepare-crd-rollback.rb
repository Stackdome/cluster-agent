#!/usr/bin/env ruby
# frozen_string_literal: true

require_relative "crd_contract"

expected_path, actual_path = ARGV
abort "usage: prepare-crd-rollback.rb <rollback-bundle.yaml> <kubectl-output.yaml>" unless expected_path && actual_path

expected = CRDContract.index_by_name(CRDContract.load_documents(expected_path), "rollback bundle")
actual = CRDContract.index_by_name(CRDContract.load_documents(actual_path), "installed CRDs")
abort "installed CRD names differ from rollback bundle" unless actual.keys.sort == expected.keys.sort

replacements = expected.map do |name, expected_crd|
  actual_crd = actual.fetch(name)
  CRDContract.verify_storage_state!(expected_crd.fetch("spec"), actual_crd, name)
  resource_version = actual_crd.dig("metadata", "resourceVersion")
  abort "#{name}: installed CRD is missing metadata.resourceVersion" unless resource_version

  replacement = Marshal.load(Marshal.dump(actual_crd))
  replacement.delete("status")
  replacement["metadata"].delete("managedFields")
  replacement["spec"] = Marshal.load(Marshal.dump(expected_crd.fetch("spec")))
  replacement
end

puts replacements.map { |replacement| YAML.dump(replacement).sub(/\A---\s*\n/, "") }.join("---\n")

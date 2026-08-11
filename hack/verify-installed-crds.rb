#!/usr/bin/env ruby
# frozen_string_literal: true

require_relative "crd_contract"

expected_path, actual_path = ARGV
abort "usage: verify-installed-crds.rb <expected-bundle.yaml> <kubectl-output.yaml>" unless expected_path && actual_path

expected = CRDContract.index_by_name(CRDContract.load_documents(expected_path), "rollback bundle")
actual = CRDContract.index_by_name(CRDContract.load_documents(actual_path), "installed CRDs")
abort "installed CRD names differ from rollback bundle" unless actual.keys.sort == expected.keys.sort

expected.each do |name, expected_crd|
  expected_spec = expected_crd.fetch("spec")
  actual_crd = actual.fetch(name)
  CRDContract.verify_storage_state!(expected_spec, actual_crd, name)
  next if CRDContract.canonical_spec(actual_crd.fetch("spec")) == CRDContract.canonical_spec(expected_spec)

  abort "#{name}: installed CRD spec differs from rollback bundle"
end

puts "installed CRD specs and storage topology exactly match the rollback bundle"

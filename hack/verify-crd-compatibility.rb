#!/usr/bin/env ruby
# frozen_string_literal: true

require "optparse"
require_relative "crd_contract"

options = {}
OptionParser.new do |parser|
  parser.on("--previous DIR") { |value| options[:previous] = value }
  parser.on("--current DIR") { |value| options[:current] = value }
end.parse!
abort "usage: verify-crd-compatibility.rb --previous DIR --current DIR" unless options.values_at(:previous, :current).all?

previous = CRDContract.index_by_name(CRDContract.load_directory(options.fetch(:previous)), "previous release")
current = CRDContract.index_by_name(CRDContract.load_directory(options.fetch(:current)), "current release")
abort "previous release contains no CRDs" if previous.empty?
abort "candidate CRD names differ from protected rollback release" unless current.keys.sort == previous.keys.sort

previous.each do |name, previous_crd|
  previous_spec = CRDContract.canonical_spec(previous_crd.fetch("spec"))
  current_spec = CRDContract.canonical_spec(current.fetch(name).fetch("spec"))
  abort "CRD #{name} spec changed; public alpha releases require exact CRD specs" unless current_spec == previous_spec
end

puts "candidate CRD specs exactly match the protected rollback release"

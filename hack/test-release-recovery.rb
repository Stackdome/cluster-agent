#!/usr/bin/env ruby
# frozen_string_literal: true

steps = %w[
  agent-image-tag
  reconciler-image-tag
  standalone-chart-push
  umbrella-chart-push
  agent-attestation
  reconciler-attestation
  standalone-chart-attestation
  umbrella-chart-attestation
]
expected = steps.each_with_index.to_h { |step, index| [step, format("sha256:%064x", index + 1)] }

def preflight!(state, expected)
  mismatches = state.select { |coordinate, digest| expected[coordinate] != digest }
  raise "existing immutable coordinate mismatch: #{mismatches.keys.join(', ')}; use a new version" unless mismatches.empty?
end

steps.each_index do |failure_index|
  state = {}
  steps.each_with_index do |step, index|
    state[step] = expected.fetch(step)
    break if index == failure_index
  end

  preflight!(state, expected)
  steps.each { |step| state[step] ||= expected.fetch(step) }
  raise "recovery missed a publication after #{steps[failure_index]}" unless state == expected
end

mismatched = {steps.first => "sha256:#{'f' * 64}"}
begin
  preflight!(mismatched, expected)
  raise "mismatched immutable coordinate unexpectedly passed preflight"
rescue RuntimeError => error
  raise unless error.message.include?("use a new version")
end

puts "release interruption recovery covered all image, chart, and attestation checkpoints"

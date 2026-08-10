#!/usr/bin/env ruby
# frozen_string_literal: true

require "base64"
require "digest"
require "fileutils"
require "json"
require "pathname"

PUBLICATION_CHECKPOINTS = %w[
  agent-image-tag
  reconciler-image-tag
  standalone-chart-push
  umbrella-chart-push
].freeze
ATTESTATION_CHECKPOINTS = %w[
  agent-attestation
  reconciler-attestation
  standalone-chart-attestation
  umbrella-chart-attestation
].freeze
ALL_CHECKPOINTS = (PUBLICATION_CHECKPOINTS + ATTESTATION_CHECKPOINTS).freeze
DIGEST_PATTERN = /\Asha256:[0-9a-f]{64}\z/

def abort_usage
  abort <<~USAGE
    usage:
      release-state.rb init <state.json>
      release-state.rb record-publication <state.json> <checkpoint> <subject-name> <digest>
      release-state.rb record-attestation <state.json> <checkpoint> <subject-name> <digest> <id> <source-bundle> <retained-bundle>
      release-state.rb complete <state.json> <release-metadata.json> <artifact-dir> <completion-marker> <checksums>
  USAGE
end

def require_digest!(digest, description)
  abort "#{description} is not a SHA-256 digest" unless digest.match?(DIGEST_PATTERN)
end

def load_state(path)
  state = JSON.parse(File.read(path))
  abort "invalid release state version" unless state == {"version" => 1, "checkpoints" => []} ||
                                               state["version"] == 1 && state["checkpoints"].is_a?(Array)
  state
rescue Errno::ENOENT
  abort "release state does not exist: #{path}"
rescue JSON::ParserError => error
  abort "invalid release state JSON: #{error.message}"
end

def atomic_write(path, contents)
  destination = Pathname.new(path)
  FileUtils.mkdir_p(destination.dirname)
  temporary = destination.dirname.join(".#{destination.basename}.tmp-#{Process.pid}")
  File.write(temporary, contents)
  File.rename(temporary, destination)
ensure
  FileUtils.rm_f(temporary) if defined?(temporary)
end

def save_state(path, state)
  atomic_write(path, JSON.pretty_generate(state) + "\n")
end

def record_checkpoint!(state, entry)
  name = entry.fetch("name")
  existing = state.fetch("checkpoints").select { |checkpoint| checkpoint["name"] == name }
  abort "release state contains duplicate checkpoint #{name}" if existing.length > 1
  return if existing.first == entry
  abort "checkpoint #{name} was already recorded with different evidence" if existing.any?

  state.fetch("checkpoints") << entry
end

def attestation_statement(bundle)
  envelope = bundle["dsseEnvelope"]
  abort "attestation bundle is missing dsseEnvelope" unless envelope.is_a?(Hash)
  payload = Base64.strict_decode64(envelope.fetch("payload"))
  JSON.parse(payload)
rescue KeyError, ArgumentError, JSON::ParserError => error
  abort "invalid attestation bundle payload: #{error.message}"
end

def select_attestation_bundle(source, expected_name, expected_digest)
  raw = File.read(source)
  bundles = begin
    [JSON.parse(raw)]
  rescue JSON::ParserError
    raw.lines.map(&:strip).reject(&:empty?).map do |line|
      JSON.parse(line)
    rescue JSON::ParserError => error
      abort "invalid attestation bundle JSON: #{error.message}"
    end
  end
  abort "attestation bundle file is empty" if bundles.empty?

  algorithm, value = expected_digest.split(":", 2)
  matches = bundles.select do |bundle|
    subjects = attestation_statement(bundle)["subject"]
    subjects == [{"name" => expected_name, "digest" => {algorithm => value}}]
  end
  abort "attestation bundle does not contain exactly one expected subject" unless matches.length == 1
  matches.first
end

def retained_bundle_path(state_path, bundle_path)
  state_directory = Pathname.new(state_path).expand_path.dirname
  bundle = Pathname.new(bundle_path).expand_path
  bundle.relative_path_from(state_directory).to_s
rescue ArgumentError
  bundle.to_s
end

def expected_checkpoints(metadata)
  coordinates = {
    "agent" => metadata.dig("images", "agent"),
    "reconciler" => metadata.dig("images", "reconciler"),
    "standalone" => metadata.dig("charts", "standalone"),
    "umbrella" => metadata.dig("charts", "umbrella"),
  }
  coordinates.each_with_object({}) do |(kind, coordinate), expected|
    abort "release metadata is missing #{kind} OCI coordinate" unless coordinate.to_s.count("@") == 1
    subject, digest = coordinate.split("@", 2)
    require_digest!(digest, "#{kind} OCI coordinate")
    publication_name = {
      "agent" => "agent-image-tag",
      "reconciler" => "reconciler-image-tag",
      "standalone" => "standalone-chart-push",
      "umbrella" => "umbrella-chart-push",
    }.fetch(kind)
    attestation_name = {
      "agent" => "agent-attestation",
      "reconciler" => "reconciler-attestation",
      "standalone" => "standalone-chart-attestation",
      "umbrella" => "umbrella-chart-attestation",
    }.fetch(kind)
    expected[publication_name] = [subject, digest]
    expected[attestation_name] = [subject, digest]
  end
end

command = ARGV.shift or abort_usage
case command
when "init"
  state_path = ARGV.shift or abort_usage
  abort_usage unless ARGV.empty?
  if File.exist?(state_path)
    load_state(state_path)
  else
    save_state(state_path, {"version" => 1, "checkpoints" => []})
  end
when "record-publication"
  state_path, name, subject, digest = ARGV
  abort_usage unless state_path && name && subject && digest && ARGV.length == 4
  abort "unknown publication checkpoint #{name}" unless PUBLICATION_CHECKPOINTS.include?(name)
  abort "publication subject name is empty" if subject.empty?
  require_digest!(digest, "publication digest")
  state = load_state(state_path)
  record_checkpoint!(state, {"name" => name, "subject" => subject, "digest" => digest})
  save_state(state_path, state)
when "record-attestation"
  state_path, name, subject, digest, attestation_id, source_bundle, retained_bundle = ARGV
  abort_usage unless ARGV.length == 7
  abort "unknown attestation checkpoint #{name}" unless ATTESTATION_CHECKPOINTS.include?(name)
  abort "attestation subject name is empty" if subject.empty?
  abort "attestation ID is empty" if attestation_id.empty?
  require_digest!(digest, "attestation digest")
  bundle = select_attestation_bundle(source_bundle, subject, digest)
  retained_contents = JSON.pretty_generate(bundle) + "\n"
  entry = {
    "name" => name,
    "subject" => subject,
    "digest" => digest,
    "attestation_id" => attestation_id,
    "bundle" => retained_bundle_path(state_path, retained_bundle),
    "bundle_sha256" => Digest::SHA256.hexdigest(retained_contents),
  }
  state = load_state(state_path)
  record_checkpoint!(state, entry)
  # Validate conflicting retries before replacing evidence retained by a prior record.
  atomic_write(retained_bundle, retained_contents)
  save_state(state_path, state)
when "complete"
  state_path, metadata_path, artifact_dir, completion_path, checksums_path = ARGV
  abort_usage unless ARGV.length == 5
  state = load_state(state_path)
  indexed = state.fetch("checkpoints").each_with_object({}) do |checkpoint, result|
    name = checkpoint["name"]
    abort "unknown release checkpoint #{name}" unless ALL_CHECKPOINTS.include?(name)
    abort "duplicate release checkpoint #{name}" if result.key?(name)
    result[name] = checkpoint
  end
  abort "release state does not contain all eight checkpoints" unless indexed.keys.sort == ALL_CHECKPOINTS.sort

  metadata = JSON.parse(File.read(metadata_path))
  expected_checkpoints(metadata).each do |name, (subject, digest)|
    checkpoint = indexed.fetch(name)
    abort "#{name} subject or digest differs from release metadata" unless checkpoint.values_at("subject", "digest") == [subject, digest]
    next unless ATTESTATION_CHECKPOINTS.include?(name)

    abort "#{name} is missing its attestation ID" if checkpoint["attestation_id"].to_s.empty?
    bundle_path = Pathname.new(checkpoint.fetch("bundle"))
    bundle_path = Pathname.new(state_path).expand_path.dirname.join(bundle_path) unless bundle_path.absolute?
    abort "#{name} retained bundle is missing" unless bundle_path.file?
    abort "#{name} retained bundle checksum differs" unless Digest::SHA256.file(bundle_path).hexdigest == checkpoint["bundle_sha256"]
    select_attestation_bundle(bundle_path, subject, digest)
  end

  root = Pathname.new(artifact_dir).expand_path
  completion_destination = Pathname.new(completion_path).expand_path
  checksum_destination = Pathname.new(checksums_path).expand_path
  completion_relative = completion_destination.relative_path_from(root)
  abort "completion marker must be inside the artifact directory" if completion_relative.to_s.start_with?("..")
  lines = Dir.glob(root.join("**", "*").to_s).sort.each_with_object([]) do |path, checksum_lines|
    pathname = Pathname.new(path)
    next unless pathname.file?
    next if pathname == checksum_destination || pathname == completion_destination

    checksum_lines << "#{Digest::SHA256.file(pathname).hexdigest}  #{pathname.relative_path_from(root)}"
  end
  lines << "#{Digest::SHA256.hexdigest("complete\n")}  #{completion_relative}"
  lines.sort!
  atomic_write(checksums_path, lines.join("\n") + "\n")
  atomic_write(completion_path, "complete\n")
else
  abort_usage
end

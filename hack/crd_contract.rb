# frozen_string_literal: true

require "yaml"

module CRDContract
  module_function

  def load_documents(path)
    document = YAML.safe_load(File.read(path), aliases: true)
    return document.fetch("items") if document.is_a?(Hash) && document["kind"] == "List"

    YAML.load_stream(File.read(path)).compact
  end

  def load_directory(directory)
    Dir.glob(File.join(directory, "*.{yaml,yml}")).sort.flat_map do |path|
      YAML.load_stream(File.read(path)).compact
    end
  end

  def index_by_name(documents, description)
    documents.each_with_object({}) do |crd, indexed|
      name = crd.dig("metadata", "name")
      abort "#{description} contains a CRD without metadata.name" unless name
      abort "#{description} contains duplicate CRD #{name}" if indexed.key?(name)

      indexed[name] = crd
    end
  end

  # Kubernetes documents these three fields as defaulting to these values.
  # No schema, conversion webhook, or version-topology differences are ignored.
  def canonical_spec(spec)
    canonical = Marshal.load(Marshal.dump(spec))
    canonical.delete("preserveUnknownFields") if canonical["preserveUnknownFields"] == false
    canonical.delete("conversion") if canonical["conversion"] == {"strategy" => "None"}
    Array(canonical["versions"]).each do |version|
      version.delete("deprecated") if version["deprecated"] == false
    end
    canonical
  end

  def version_topology(spec)
    Array(spec["versions"]).map do |version|
      {
        "name" => version["name"],
        "served" => version["served"],
        "storage" => version["storage"],
      }
    end
  end

  def storage_versions(spec, name)
    versions = version_topology(spec).select { |version| version["storage"] }.map { |version| version["name"] }
    abort "#{name}: expected exactly one storage version" unless versions.length == 1
    versions
  end

  def verify_storage_state!(expected_spec, actual_crd, name)
    expected_topology = version_topology(expected_spec)
    actual_topology = version_topology(actual_crd.fetch("spec"))
    abort "#{name}: installed version topology differs from rollback bundle" unless actual_topology == expected_topology

    expected_storage = storage_versions(expected_spec, name)
    stored_versions = Array(actual_crd.dig("status", "storedVersions"))
    abort "#{name}: status.storedVersions differs from rollback storage topology" unless stored_versions.sort == expected_storage.sort
  end
end

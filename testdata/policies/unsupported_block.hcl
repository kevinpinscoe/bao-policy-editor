# A top-level block type this domain model has no representation for.
# BPE must preserve it in Document.Bytes() and flag Document.Unsupported,
# never silently drop it.
path "secret/data/example" {
  capabilities = ["read"]
}

unknown_block "example" {
  some_attribute = "value"
}

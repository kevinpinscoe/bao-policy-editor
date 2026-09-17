# An attribute inside a `path` block that FSM-11's domain model does not
# recognize. Must be preserved verbatim and flagged as unsupported, never
# silently dropped.
path "secret/data/example" {
  capabilities  = ["read"]
  future_option = "not yet a real OpenBao attribute"
}

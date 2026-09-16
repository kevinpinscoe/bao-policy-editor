# The expiration value is not a valid RFC 3339 timestamp.
path "secret/data/example" {
  capabilities = ["read"]
  expiration   = "not-a-timestamp"
}

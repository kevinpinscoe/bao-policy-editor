# A representative, fully-supported policy exercising every attribute
# FSM-11 decodes.
path "secret/data/team-a/*" {
  capabilities = ["create", "read", "update"]
  comment      = "Team A's own KV v2 secrets"

  required_parameters = ["owner"]

  allowed_parameters = {
    "owner" = []
    "ttl"   = ["1h", "24h"]
    "env"   = []
  }

  denied_parameters = {
    "root" = []
  }
}

path "secret/metadata/team-a/*" {
  capabilities = ["list", "read"]
}

path "sys/policies/acl/team-a-*" {
  capabilities = ["read", "list"]
  expiration   = "2030-01-01T00:00:00Z"
}

# This is the rule valid.hcl's first path block used to be, before FSM-12
# fixed it: `owner` is required, but `allowed_parameters` is a whitelist
# that never lists `owner` (and has no "*" wildcard either), so no request
# could ever satisfy this rule. FSM-11's decoder had no way to catch this
# — it only decodes HCL, it does not check whether the result makes sense
# — so the mistake shipped as part of "fully-supported" example content
# until FSM-12's Validate actually exercised it.
#
# Preserved here, unmodified, as a dedicated negative fixture so the
# specific contradiction diagnostic stays under regression coverage even
# though valid.hcl itself no longer exhibits it. See
# TestValidate_Fixture_ContradictoryRequiredParameter in validate_test.go.
path "secret/data/team-a/*" {
  capabilities = ["create", "read", "update"]
  comment      = "Team A's own KV v2 secrets"

  required_parameters = ["owner"]

  allowed_parameters = {
    "ttl" = ["1h", "24h"]
    "env" = []
  }

  denied_parameters = {
    "root" = []
  }
}

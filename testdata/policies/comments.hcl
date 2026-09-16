# Leading comment on the file.
path "secret/data/example" {
  # A comment above the capabilities attribute.
  capabilities = ["read"] // trailing comment on the same line

  /* A block comment
     spanning multiple lines. */
  comment = "documented rule"
}

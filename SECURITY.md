# Security policy

## Supported versions

There are no tagged releases yet. Security fixes are maintained on main; older
commits and forks are not supported. Review deployment changes before updating.

## Reporting a vulnerability

Use GitHub private vulnerability reporting: open the repository's Security tab
and select **Report a vulnerability**. If the button is unavailable, use the
contact details on Victoroide's GitHub profile to arrange a private report.
Do not disclose an exploit in an issue, pull request or public discussion.

Include the affected commit or image tag, expected and actual behavior, a minimal
reproduction, impact and any suggested mitigation. Use synthetic documents and
redact service tokens, credentials, signed download links and sensitive data.
Do not test against deployments or documents you do not own.

## Response timeline

Victoroide aims to acknowledge reports within three business days and provide an
initial assessment within seven business days. Confirmed reports receive a fix
plan or status update within 30 days. Fix timing depends on impact and deployment
constraints; urgent issues may receive a mitigation before a release. Coordinate
disclosure after a fix or mitigation is available.

## Scope and deployment boundaries

- Service token authentication: every route except the health probe requires the
  shared token in deployed mode. Keep it secret, rotate it after exposure and
  restrict direct access to the service.
- Document isolation: callers must send the correct document allowlist for
  restricted queries. A shared service token is not per-user authorization.
- S3 presigned downloads: a signed link grants temporary file access to anyone
  holding it. Avoid logging or sharing links and restrict storage permissions.
- Uploaded file parsing: malformed documents, parser resource exhaustion and
  request size bypasses are in scope. Use a small synthetic file when reporting.
- Retrieved text: documents may contain hostile instructions. Reports about
  unauthorized document access, data disclosure or unsafe handling of retrieved
  content are in scope.

Offline mode is for local development only. It is not an authentication bypass
that is safe to enable in a deployed service.

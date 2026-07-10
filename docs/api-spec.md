# API explorer

An interactive reference for the **registry-management REST API** (the BFF) —
every endpoint the dashboard, CLI, and Terraform use. It is **generated from the
service route table** (`services/management/cmd/openapi-gen`), so the paths,
methods, path parameters, and authentication requirement always match the code;
a CI drift-guard fails the build if the committed spec falls out of date.

!!! note "Scope"
    This spec describes the **management BFF** surface. Paths, methods, path
    parameters, and the auth requirement are exact; request/query and response
    body schemas are being enriched incrementally (see the note on the
    [API & automation](api-reference.md#openapi-specification) page). The
    identity/session API (login, SSO, MFA, API keys) is served separately by
    `registry-auth`. For the auth model and a worked `curl`, see
    [API & automation](api-reference.md).

The raw document is published at
[`openapi.json`](openapi.json) if you want to import it into Postman, an SDK
generator, or your own tooling.

!!swagger openapi.json!!

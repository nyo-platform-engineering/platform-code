# Policy

Defines who can access each HTTP endpoint.

- `policies` maps a method and route to either public access or a required permission.
- `Enforce` authenticates protected requests, checks tenant and permission, and puts the principal in the request context.
- `ValidateRoutes` checks that registered routes and policy entries match.

[Routes](../routes/routes.go) wire handlers; this package owns access decisions. Add a policy entry whenever you add a route. Unknown API paths cannot fall through to the public frontend.

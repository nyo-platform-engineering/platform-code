// Package auth authenticates browser sessions and resolves verified OAuth
// identities into trusted organization-scoped principals.
//
// Authentication establishes identity. Authorization remains in package policy,
// while database schema and connection ownership remain in package database.
package auth

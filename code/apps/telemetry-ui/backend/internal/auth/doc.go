// Package auth exposes authentication, session, and organization-access
// contracts to the rest of the backend.
//
// Provider handling, persistence, validation, and cookie mechanics stay in the
// nested internal package. Authorization remains in package policy, while
// database schema and connection ownership remain in package database.
package auth

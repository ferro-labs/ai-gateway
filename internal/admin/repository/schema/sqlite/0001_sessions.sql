CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	token_hash TEXT UNIQUE NOT NULL,
	subject TEXT NOT NULL,
	credential_id TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_seen_at DATETIME NULL,
	expires_at DATETIME NOT NULL
);

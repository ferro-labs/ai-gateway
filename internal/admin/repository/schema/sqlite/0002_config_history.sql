CREATE TABLE IF NOT EXISTS config_history (
	version INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
);

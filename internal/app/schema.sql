
CREATE TABLE IF NOT EXISTS scopes (
    scope TEXT PRIMARY KEY, initialized INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS books (
    scope TEXT NOT NULL, book_id TEXT NOT NULL,
    title TEXT NOT NULL, author TEXT NOT NULL, isbn TEXT NOT NULL, isbn13 TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'discovered', next_check REAL NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '',
    release_id TEXT NOT NULL DEFAULT '', submitted_at REAL,
    reason TEXT NOT NULL DEFAULT '', created_at REAL NOT NULL,
    previewed_at REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, book_id)
);
CREATE INDEX IF NOT EXISTS books_due ON books(scope, status, next_check);
CREATE TABLE IF NOT EXISTS attempts (
    id INTEGER PRIMARY KEY, scope TEXT NOT NULL, book_id TEXT NOT NULL,
    source TEXT NOT NULL, release_id TEXT NOT NULL, outcome TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_book ON attempts(scope, book_id);
CREATE TABLE IF NOT EXISTS source_aliases (
    scope TEXT NOT NULL, alias_id TEXT NOT NULL, book_id TEXT NOT NULL,
    PRIMARY KEY (scope, alias_id),
    FOREIGN KEY (scope, book_id) REFERENCES books(scope, book_id)
);
CREATE INDEX IF NOT EXISTS aliases_book ON source_aliases(scope, book_id);
PRAGMA user_version=2;

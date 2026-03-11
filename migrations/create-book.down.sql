-- Migration: 003_create_books (rollback)
-- Drops the books table. book_index_events (004) must be rolled back first
-- because it holds a foreign key to this table.
DROP TABLE IF EXISTS books;
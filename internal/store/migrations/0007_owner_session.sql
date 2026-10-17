-- Owning server session for a queue lease.
-- Each factoryd process holds a session advisory lock on its own id
-- for the life of a dedicated connection. The leader requeues a
-- reviewing run only when that lock is not held. NULL means no owner.
-- The lock drops when the connection closes, including a crash, and
-- it stays held when only the leader connection closes.

ALTER TABLE run_queue
ADD COLUMN IF NOT EXISTS owner_session text;

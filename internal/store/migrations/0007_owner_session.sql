-- Owning server session for a queue lease.
-- Each factoryd process holds a session advisory lock on its own id.
-- The leader requeues a reviewing run only when that id is set and
-- the lock is not held. NULL is a lease from a process that does not
-- know this column. The sweep leaves it for ExpireLeases.

ALTER TABLE run_queue
ADD COLUMN IF NOT EXISTS owner_session text;

-- A service is a compose project now, made of containers. An action names its service, and
-- container the one container it is for, or null for every container of the service. Two actions
-- deploy and remove a managed service; a remove's delete_data says whether the daemon deletes the
-- service's folder, data and all, or keeps it aside.
ALTER TABLE actions ADD COLUMN container TEXT;
ALTER TABLE actions ADD COLUMN delete_data INTEGER CHECK (delete_data IN (0, 1));

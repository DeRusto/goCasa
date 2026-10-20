-- The Weather page reads more of the forecast than the cache holds: refetch now.
UPDATE integrations SET next_sync_at = 0 WHERE type = 'open_meteo';

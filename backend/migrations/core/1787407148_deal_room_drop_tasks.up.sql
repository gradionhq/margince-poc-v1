-- The shared to-do list leaves the Deal Room. Sharing files and discussing
-- them is the product; a list of work owed was never agreed and is removed
-- before any installation holds one.
SET LOCAL lock_timeout = '5s';
DROP TABLE IF EXISTS deal_room_task;

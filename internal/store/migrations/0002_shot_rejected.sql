-- Удар, который сервис не признал игровым: детектор открыл его по движению,
-- в котором биток не участвовал (шар катнули рукой или кием помощника).
-- В статистику такой удар не идёт, но остаётся в базе вместе с событиями —
-- чтобы решение можно было проверить и пересмотреть.
ALTER TABLE shots DROP CONSTRAINT shots_status_check;
ALTER TABLE shots ADD CONSTRAINT shots_status_check
    CHECK (status IN ('open', 'closed', 'aborted', 'rejected'));
ALTER TABLE shots ADD COLUMN reject_reason text;

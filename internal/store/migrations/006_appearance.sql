-- Оформление сайта, которое пользователь выбирает в настройках профиля:
-- стиль (базовый или праздничный «Хэллоуин») и тема (как в системе, светлая, тёмная).
ALTER TABLE users
    ADD COLUMN style  text NOT NULL DEFAULT 'base' CHECK (style IN ('base', 'halloween')),
    ADD COLUMN scheme text NOT NULL DEFAULT 'auto' CHECK (scheme IN ('auto', 'light', 'dark'));

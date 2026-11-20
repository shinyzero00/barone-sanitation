WITH people(email, name, bio) AS (VALUES
    ('dick-barone@barone.invalid', 'Дик Бароне', 'Руководит семейной компанией и помогает бизнесу подобрать график вывоза.'),
    ('paulie-gualtieri@barone.invalid', 'Поли Гуалтьери', 'Подбирает контейнеры для ремонта и организует вывоз крупногабаритных отходов.'),
    ('tony-soprano@barone.invalid', 'Тони Сопрано', 'Консультирует по коммерческим договорам и вывозу отходов для ресторанов.')
), new_users AS (
    INSERT INTO users(name,email,password_hash,role)
    SELECT name,email,$1,'consultant' FROM people RETURNING user_id,email
), new_consultants AS (
    INSERT INTO consultants(user_id,bio)
    SELECT user_id,bio FROM new_users JOIN people USING(email) RETURNING consultant_id,user_id
), team AS (
    SELECT consultant_id,email FROM new_consultants JOIN new_users USING(user_id)
), new_types AS (
    INSERT INTO consultation_types(title,description,duration_minutes,price) VALUES
        ('Маршрут для вашего бизнеса', 'Подберём частоту и время вывоза для магазина или офиса.', 45, 75.00),
        ('Контейнер на время ремонта', 'Выберем объём, место установки и срок аренды контейнера.', 30, 45.00),
        ('Вывоз после переезда', 'Составим план вывоза мебели, техники и других крупногабаритных отходов.', 30, 45.00),
        ('Разбор договора на вывоз', 'Разберём график, объёмы и условия обслуживания.', 60, 95.00),
        ('Раздельный сбор для ресторана', 'Подберём схему вывоза тары, картона и пищевых отходов.', 30, 55.00)
    RETURNING consultation_type_id,title
), assignments(email, title) AS (VALUES
    ('dick-barone@barone.invalid', 'Маршрут для вашего бизнеса'),
    ('dick-barone@barone.invalid', 'Разбор договора на вывоз'),
    ('paulie-gualtieri@barone.invalid', 'Контейнер на время ремонта'),
    ('paulie-gualtieri@barone.invalid', 'Вывоз после переезда'),
    ('tony-soprano@barone.invalid', 'Разбор договора на вывоз'),
    ('tony-soprano@barone.invalid', 'Раздельный сбор для ресторана')
), new_offerings AS (
    INSERT INTO consultation_offerings(consultant_id,consultation_type_id)
    SELECT consultant_id,consultation_type_id FROM assignments JOIN team USING(email) JOIN new_types USING(title)
)
INSERT INTO work_intervals(consultant_id,starts_at,ends_at)
SELECT consultant_id,
    (day.local_date + time '10:00') AT TIME ZONE 'America/New_York',
    (day.local_date + time '16:00') AT TIME ZONE 'America/New_York'
FROM team CROSS JOIN generate_series(1,14) AS n(days)
CROSS JOIN LATERAL (SELECT (now() AT TIME ZONE 'America/New_York')::date + n.days AS local_date) AS day
WHERE extract(isodow FROM day.local_date) <= 5;

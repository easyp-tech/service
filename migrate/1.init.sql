-- up
create table plugins
(
    id                     uuid      not null default gen_random_uuid(),
    name                   text,
    created_at             timestamp not null default now(),

    unique (name),
    primary key (id)
);

-- down
drop table plugins;
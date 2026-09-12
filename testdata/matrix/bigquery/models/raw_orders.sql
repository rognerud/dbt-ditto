-- bigquery-emulator does not implement load jobs, so this project has no seed:
-- its documented root is a model that materialises the rows inline.
select 1 as order_id, 10 as customer_id, "completed" as status, 1500 as amount_cents
union all
select 2, 11, "pending", 2400
union all
select 3, 10, "returned", 900

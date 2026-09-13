select
    invoice_id,
    order_id,
    invoice_total_cents,
    issued_on
from {{ source('billing', 'raw_invoices') }}

-- Dropped in dependency order: payments and invoices reference connections and
-- organizations, and finance_invoice references itself through the credit-note
-- pointer, which the table drop takes with it.
DROP TABLE IF EXISTS finance_payment;
DROP TABLE IF EXISTS finance_invoice;
DROP TABLE IF EXISTS finance_customer_link;
DROP TABLE IF EXISTS finance_external_customer;
DROP TABLE IF EXISTS finance_connection;

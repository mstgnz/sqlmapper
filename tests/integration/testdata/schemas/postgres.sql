--
-- PostgreSQL database dump
--

\restrict 3PlfTGxMl7mxW7SkkHvzTMO3ETx6KySMepKEknegDz35E8GYMPiVKlNvQ0invQW

-- Dumped from database version 17.11 (Debian 17.11-1.pgdg13+2)
-- Dumped by pg_dump version 17.11 (Debian 17.11-1.pgdg13+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: 
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: order_status; Type: TYPE; Schema: public; Owner: postgres
--

CREATE TYPE public.order_status AS ENUM (
    'draft',
    'placed',
    'shipped',
    'cancelled'
);


ALTER TYPE public.order_status OWNER TO postgres;

--
-- Name: archive_orders(date); Type: PROCEDURE; Schema: public; Owner: postgres
--

CREATE PROCEDURE public.archive_orders(IN cutoff date)
    LANGUAGE plpgsql
    AS $$
BEGIN
  DELETE FROM orders WHERE placed_on < cutoff;
END;
$$;


ALTER PROCEDURE public.archive_orders(IN cutoff date) OWNER TO postgres;

--
-- Name: order_total(bigint); Type: FUNCTION; Schema: public; Owner: postgres
--

CREATE FUNCTION public.order_total(p_order_id bigint) RETURNS numeric
    LANGUAGE sql
    AS $$ SELECT COALESCE(SUM(qty), 0) FROM order_lines WHERE order_id = p_order_id $$;


ALTER FUNCTION public.order_total(p_order_id bigint) OWNER TO postgres;

--
-- Name: touch_customer(); Type: FUNCTION; Schema: public; Owner: postgres
--

CREATE FUNCTION public.touch_customer() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  NEW.created_at = now();
  RETURN NEW;
END;
$$;


ALTER FUNCTION public.touch_customer() OWNER TO postgres;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: customers; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.customers (
    id bigint NOT NULL,
    email character varying(255) NOT NULL,
    full_name text,
    is_active boolean DEFAULT true NOT NULL,
    score numeric(10,2) DEFAULT 0,
    tags text[],
    meta jsonb,
    referred_by bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT customers_score_check CHECK ((score >= (0)::numeric))
);


ALTER TABLE public.customers OWNER TO postgres;

--
-- Name: TABLE customers; Type: COMMENT; Schema: public; Owner: postgres
--

COMMENT ON TABLE public.customers IS 'people who buy things';


--
-- Name: COLUMN customers.email; Type: COMMENT; Schema: public; Owner: postgres
--

COMMENT ON COLUMN public.customers.email IS 'login address, unique';


--
-- Name: COLUMN customers.score; Type: COMMENT; Schema: public; Owner: postgres
--

COMMENT ON COLUMN public.customers.score IS 'loyalty points';


--
-- Name: active_customers; Type: VIEW; Schema: public; Owner: postgres
--

CREATE VIEW public.active_customers AS
 SELECT id,
    email,
    score
   FROM public.customers
  WHERE is_active;


ALTER VIEW public.active_customers OWNER TO postgres;

--
-- Name: customers_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.customers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.customers_id_seq OWNER TO postgres;

--
-- Name: customers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.customers_id_seq OWNED BY public.customers.id;


--
-- Name: order_lines; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.order_lines (
    order_id bigint NOT NULL,
    line_no integer NOT NULL,
    sku character varying(64) NOT NULL,
    qty integer DEFAULT 1 NOT NULL
);


ALTER TABLE public.order_lines OWNER TO postgres;

--
-- Name: orders; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.orders (
    id bigint NOT NULL,
    customer_id bigint NOT NULL,
    status public.order_status DEFAULT 'draft'::public.order_status NOT NULL,
    total numeric(12,2) NOT NULL,
    placed_on date NOT NULL,
    note character varying(200) DEFAULT 'none'::character varying,
    CONSTRAINT orders_total_check CHECK ((total >= (0)::numeric))
);


ALTER TABLE public.orders OWNER TO postgres;

--
-- Name: TABLE orders; Type: COMMENT; Schema: public; Owner: postgres
--

COMMENT ON TABLE public.orders IS 'one row per order';


--
-- Name: orders_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.orders_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.orders_id_seq OWNER TO postgres;

--
-- Name: orders_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.orders_id_seq OWNED BY public.orders.id;


--
-- Name: ticket_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.ticket_seq
    START WITH 100
    INCREMENT BY 5
    MINVALUE 100
    MAXVALUE 999999
    CACHE 20;


ALTER SEQUENCE public.ticket_seq OWNER TO postgres;

--
-- Name: customers id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.customers ALTER COLUMN id SET DEFAULT nextval('public.customers_id_seq'::regclass);


--
-- Name: orders id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.orders ALTER COLUMN id SET DEFAULT nextval('public.orders_id_seq'::regclass);


--
-- Name: customers customers_email_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_email_key UNIQUE (email);


--
-- Name: customers customers_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_pkey PRIMARY KEY (id);


--
-- Name: order_lines order_lines_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.order_lines
    ADD CONSTRAINT order_lines_pkey PRIMARY KEY (order_id, line_no);


--
-- Name: orders orders_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);


--
-- Name: orders orders_unique_day; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_unique_day UNIQUE (customer_id, placed_on);


--
-- Name: idx_customers_lower_email; Type: INDEX; Schema: public; Owner: postgres
--

CREATE UNIQUE INDEX idx_customers_lower_email ON public.customers USING btree (email);


--
-- Name: idx_lines_sku_qty; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_lines_sku_qty ON public.order_lines USING btree (sku, qty);


--
-- Name: idx_orders_customer; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_orders_customer ON public.orders USING btree (customer_id);


--
-- Name: idx_orders_open; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_orders_open ON public.orders USING btree (placed_on) WHERE (status <> 'shipped'::public.order_status);


--
-- Name: customers customers_touch; Type: TRIGGER; Schema: public; Owner: postgres
--

CREATE TRIGGER customers_touch BEFORE INSERT ON public.customers FOR EACH ROW EXECUTE FUNCTION public.touch_customer();


--
-- Name: customers customers_referred_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_referred_by_fkey FOREIGN KEY (referred_by) REFERENCES public.customers(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: order_lines order_lines_order_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.order_lines
    ADD CONSTRAINT order_lines_order_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;


--
-- Name: orders orders_customer_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_customer_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON UPDATE RESTRICT ON DELETE CASCADE;


--
-- Name: TABLE customers; Type: ACL; Schema: public; Owner: postgres
--

GRANT SELECT ON TABLE public.customers TO reporting;


--
-- Name: TABLE orders; Type: ACL; Schema: public; Owner: postgres
--

GRANT SELECT,INSERT ON TABLE public.orders TO reporting;


--
-- PostgreSQL database dump complete
--

\unrestrict 3PlfTGxMl7mxW7SkkHvzTMO3ETx6KySMepKEknegDz35E8GYMPiVKlNvQ0invQW


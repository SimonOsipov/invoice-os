#!/usr/bin/env python3
"""Generate spreadsheet layouts with a known column mapping for the CSV mapping model test.

Writes $DATA/layouts.json and one CSV per layout under $DATA/csv/. Header spellings in the
"software" layouts are modelled on common accounting-software exports; every value is synthetic.
The answer key follows the importer's fields (frontend/app/src/data.tsx CANON): one row per
invoice line, invoice-level values repeated on every line, and a line total is never a subtotal.
"""
import csv, datetime as dt, io, json, os, random, re

DATA = os.environ["DATA"]
SEED = int(os.environ.get("SEED", "20260914"))
FIELDS = ["invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat",
          "total", "line_description", "line_quantity", "line_unit_price", "invoice_kind", "tax_currency_code",
          "due_date", "issue_time", "tax_point_date", "payment_status", "buyer_email", "buyer_telephone",
          "buyer_street", "buyer_city", "buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga",
          "line_total", "line_tax", "line_tax_category", "line_hsn_code", "line_isic_code",
          "line_product_category", "line_service_category", "line_sellers_item_identification",
          "line_price_unit", "line_tax_percent", "line_base_quantity"]
FIELD_OF = {"inv_no": "invoice_number", "date": "issue_date", "datetime": "issue_date", "buyer_tin": "buyer_tin",
            "buyer_name": "buyer_name", "currency": "currency", "subtotal": "subtotal", "vat": "vat", "total": "total",
            "line_desc": "line_description", "line_desc2": "line_description", "qty": "line_quantity",
            "unit_price": "line_unit_price", "due_date": "due_date", "line_total": "line_total", "line_tax": "line_tax",
            "vat_rate": "line_tax_percent", "email": "buyer_email", "phone": "buyer_telephone",
            "address": "buyer_street", "tax_code": "line_tax_category", "pay_status": "payment_status",
            **{k: k for k in ("invoice_kind", "tax_currency_code", "issue_time", "tax_point_date", "buyer_city",
                              "buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga", "line_hsn_code",
                              "line_isic_code", "line_product_category", "line_service_category",
                              "line_sellers_item_identification", "line_price_unit", "line_base_quantity",
                              "line_tax_category")}}
KIND_OF = {"invoice_number": "inv_no", "issue_date": "date", "buyer_tin": "buyer_tin", "buyer_name": "buyer_name",
           "currency": "currency", "subtotal": "subtotal", "vat": "vat", "total": "total",
           "line_description": "line_desc", "line_quantity": "qty", "line_unit_price": "unit_price"}
LINE_KINDS = {"line_desc", "line_desc2", "qty", "unit_price", "line_total", "line_tax", "line_tax_category",
              "line_hsn_code", "line_isic_code", "line_product_category", "line_service_category",
              "line_sellers_item_identification", "line_price_unit", "line_base_quantity"}

# --- software-style layouts: (header, kind); `single` means one line per invoice ---------------
SOFTWARE = [
    ("zoho", False, [("Invoice Date", "date"), ("Invoice ID", "internal_id"), ("Invoice Number", "inv_no"),
                     ("Invoice Status", "status"), ("Customer ID", "customer_id"), ("Customer Name", "buyer_name"),
                     ("Customer Tax ID", "buyer_tin"), ("Currency Code", "currency"), ("Exchange Rate", "exchange_rate"),
                     ("Item Name", "line_desc"), ("Item Desc", "line_desc2"), ("Quantity", "qty"),
                     ("Item Price", "unit_price"), ("Item Tax", "line_tax"), ("Item Tax %", "vat_rate"),
                     ("SubTotal", "subtotal"), ("Tax Total", "vat"), ("Total", "total"), ("Balance", "balance"),
                     ("Due Date", "due_date")]),
    ("quickbooks_detail", False, [("Type", "type"), ("Date", "date"), ("Num", "inv_no"), ("Name", "buyer_name"),
                                  ("Memo/Description", "line_desc"), ("Qty", "qty"), ("Sales Price", "unit_price"),
                                  ("Amount", "line_total"), ("Balance", "balance")]),
    ("quickbooks_import", False, [("InvoiceNo", "inv_no"), ("Customer", "buyer_name"), ("InvoiceDate", "date"),
                                  ("DueDate", "due_date"), ("Terms", "terms"), ("Location", "branch"), ("Memo", "memo"),
                                  ("Item(Product/Service)", "line_desc"), ("ItemDescription", "line_desc2"),
                                  ("ItemQuantity", "qty"), ("ItemRate", "unit_price"), ("ItemAmount", "line_total"),
                                  ("ItemTaxCode", "tax_code"), ("ItemTaxAmount", "line_tax"), ("Currency", "currency")]),
    ("sage_audit", True, [("Type", "type_si"), ("Account", "customer_id"), ("Date", "date"), ("Ref", "inv_no"),
                          ("Ex.Ref", "po_no"), ("Details", "line_desc"), ("Net", "subtotal"), ("Tax", "vat"),
                          ("T/C", "tax_code"), ("Gross", "total")]),
    ("odoo", False, [("Number", "inv_no"), ("Invoice/Bill Date", "date"), ("Partner", "buyer_name"),
                     ("Partner/Tax ID", "buyer_tin"), ("Currency", "currency"), ("Untaxed Amount", "subtotal"),
                     ("Tax", "vat"), ("Total", "total"), ("Invoice lines/Label", "line_desc"),
                     ("Invoice lines/Quantity", "qty"), ("Invoice lines/Unit Price", "unit_price"),
                     ("Invoice lines/Subtotal", "line_total"), ("Payment Status", "pay_status"), ("Due Date", "due_date"),
                     ("Salesperson", "salesperson")]),
    ("tally_register", True, [("Date", "date"), ("Particulars", "buyer_name"), ("Voucher Type", "voucher_type"),
                              ("Voucher No.", "inv_no"), ("Sales Accounts", "subtotal"), ("Output VAT @7.5%", "vat"),
                              ("Gross Total", "total")]),
    ("handmade_excel", False, [("S/N", "serial"), ("INVOICE NO", "inv_no"), ("DATE", "date"), ("CUSTOMER", "buyer_name"),
                               ("CUSTOMER TIN", "buyer_tin"), ("DESCRIPTION", "line_desc"), ("QTY", "qty"),
                               ("RATE (₦)", "unit_price"), ("AMOUNT (₦)", "line_total"), ("VAT (7.5%)", "vat"),
                               ("TOTAL (₦)", "total")]),
    ("einvoice_template", False, [("Invoice Number", "inv_no"), ("Issue Date", "date"), ("Buyer Name", "buyer_name"),
                                  ("Buyer TIN", "buyer_tin"), ("Buyer Address", "address"), ("Currency Code", "currency"),
                                  ("Item Description", "line_desc"), ("Quantity", "qty"), ("Unit Price", "unit_price"),
                                  ("Line Extension Amount", "line_total"), ("Tax Exclusive Amount", "subtotal"),
                                  ("Tax Amount", "vat"), ("Payable Amount", "total")]),
    ("sap_billing", True, [("Billing Document", "inv_no"), ("Billing Date", "date"), ("Sold-To Party", "customer_num"),
                           ("Name of Sold-To", "buyer_name"), ("Tax Number 1", "buyer_tin"), ("Doc. Currency", "currency"),
                           ("Net Value", "subtotal"), ("Tax Amount", "vat"), ("Material Description", "line_desc"),
                           ("Billed Quantity", "qty"), ("Net Price", "unit_price"), ("Sales Document", "po_no")]),
    ("pos_receipts", False, [("Receipt No", "inv_no"), ("Date/Time", "datetime"), ("Cashier", "salesperson"),
                             ("Customer", "buyer_name"), ("Item", "line_desc"), ("Qty", "qty"), ("Price", "unit_price"),
                             ("Line Total", "line_total"), ("VAT", "vat"), ("Grand Total", "total"),
                             ("Payment Method", "pay_method")]),
    ("services_firm", True, [("Invoice #", "inv_no"), ("Invoice Date", "date"), ("Due Date", "due_date"),
                             ("Our TIN", "seller_tin"), ("Client", "buyer_name"), ("Client TIN", "buyer_tin"),
                             ("Service", "line_desc"), ("Hours", "qty"), ("Hourly Rate", "unit_price"), ("Fee", "subtotal"),
                             ("VAT", "vat"), ("Total Payable", "total"), ("Paid", "paid"), ("Balance Due", "balance")]),
    ("minimal", True, [("Invoice", "inv_no"), ("Date", "date"), ("Customer", "buyer_name"), ("Amount", "total")]),
    ("wht_register", True, [("Doc No", "inv_no"), ("Doc Date", "date"), ("Customer Name", "buyer_name"),
                            ("Customer TIN", "buyer_tin"), ("Invoice Amount (incl. VAT)", "total"),
                            ("VAT Amount", "vat"), ("WHT Amount", "wht"), ("Net Payable", "net_payable"),
                            ("Currency", "currency")]),
]

# --- composed layouts -------------------------------------------------------------------------
SYN = {
    "invoice_number": ["Invoice No", "Invoice Number", "Invoice #", "Inv No", "Inv. No.", "InvoiceNo", "Document No",
                       "Doc Number", "Bill No", "Voucher No", "Invoice Ref", "Sales Invoice No"],
    "issue_date": ["Date", "Invoice Date", "Issue Date", "Txn Date", "Transaction Date", "Doc Date", "Posting Date",
                   "Date Issued", "Billing Date"],
    "buyer_tin": ["Customer TIN", "Buyer TIN", "Client TIN", "TIN", "Customer Tax ID", "Tax ID", "Customer TIN No.",
                  "Buyer Tax Identification Number"],
    "buyer_name": ["Customer", "Customer Name", "Client", "Client Name", "Buyer", "Buyer Name", "Bill To", "Sold To",
                   "Party Name"],
    "currency": ["Currency", "Ccy", "Curr", "Currency Code", "Invoice Currency"],
    "subtotal": ["Subtotal", "Sub Total", "Net Amount", "Amount Before Tax", "Taxable Amount", "Amount Excl. VAT",
                 "Untaxed Amount", "Value Before VAT"],
    "vat": ["VAT", "VAT Amount", "Tax Amount", "Output VAT", "VAT (7.5%)", "Total VAT", "Sales Tax"],
    "total": ["Total", "Grand Total", "Invoice Total", "Total Amount", "Amount Due", "Total Payable",
              "Amount Incl. VAT", "Invoice Amount"],
    "line_description": ["Description", "Item", "Item Description", "Product/Service", "Particulars", "Service",
                         "Product", "Goods/Services"],
    "line_quantity": ["Qty", "Quantity", "Units", "No. of Units", "Qty Sold", "Hours"],
    "line_unit_price": ["Unit Price", "Rate", "Price", "Unit Cost", "Sales Price", "Price Each", "Price per Unit"],
}
P_FIELD = {"issue_date": .95, "buyer_name": .9, "buyer_tin": .6, "currency": .5, "subtotal": .6, "vat": .7,
           "total": .85, "line_description": .8, "line_quantity": .6, "line_unit_price": .55}
FILLERS = [("Status", "status"), ("Customer Email", "email"), ("Phone", "phone"), ("Branch", "branch"),
           ("Salesperson", "salesperson"), ("Terms", "terms"), ("Discount", "discount"), ("WHT", "wht"),
           ("Exchange Rate", "exchange_rate"), ("Customer Address", "address"), ("RC Number", "rc_no"), ("Memo", "memo")]

BUYERS = ["Lagos Harbour Logistics Ltd", "Kano Agro Mills Plc", "Delta Cable Works Ltd", "Abuja Office Supplies Ltd",
          "Enugu Pharma Distributors", "Ibadan Print House Ltd", "Port Harcourt Marine Services", "Jos Solar Ltd",
          "Calabar Fresh Farms", "Owerri Tiles and Paints Ltd", "Kaduna Textile Hub", "Benin Auto Parts Ltd"]
ITEMS = [("Diesel (litres)", "AGO diesel delivered to site"), ("Cement 50kg", "Portland cement bags"),
         ("Consulting hours", "Tax advisory consulting"), ("Printer toner", "Toner cartridge, black"),
         ("Freight Lagos-Kano", "Road haulage, 20ft container"), ("Laptop repair", "Motherboard replacement"),
         ("Office rent", "Monthly office rent"), ("Security services", "Guard services, monthly"),
         ("Bottled water (carton)", "75cl x 12 carton"), ("Solar panel 300W", "Monocrystalline panel")]
# NRS columns (D10): header synonyms per new field, as (cell kind, headers). Cells are fixed per buyer or item.
NRS_SYN = {
    "invoice_kind": ("invoice_kind", ["Buyer Type", "Customer Type", "Sale Type"]),
    "tax_currency_code": ("tax_currency_code", ["Tax Currency", "Tax Currency Code", "VAT Currency"]),
    "due_date": ("due_date", ["Due Date", "Payment Due", "Due On"]),
    "issue_time": ("issue_time", ["Issue Time", "Invoice Time", "Time"]),
    "tax_point_date": ("tax_point_date", ["Tax Point Date", "Supply Date", "Date of Supply"]),
    "payment_status": ("pay_status", ["Payment Status", "Pay Status", "Paid Status"]),
    "buyer_email": ("email", ["Customer Email", "Buyer Email", "Email"]),
    "buyer_telephone": ("phone", ["Phone", "Customer Phone", "Buyer Telephone", "Tel"]),
    "buyer_street": ("address", ["Street", "Bill To Address", "Customer Address", "Address"]),
    "buyer_city": ("buyer_city", ["City", "Customer City", "Town"]),
    "buyer_postal_zone": ("buyer_postal_zone", ["Postal Code", "Zip", "Post Code"]),
    "buyer_country": ("buyer_country", ["Country", "Customer Country"]),
    "buyer_state": ("buyer_state", ["State", "Customer State"]),
    "buyer_lga": ("buyer_lga", ["LGA", "Local Government", "Customer LGA"]),
    "line_total": ("line_total", ["Line Total", "Line Amount", "Item Amount"]),
    "line_tax": ("line_tax", ["Line Tax", "Item Tax", "Tax on Line"]),
    "line_tax_category": ("line_tax_category", ["Tax Category", "Tax Type", "Item Tax Category"]),
    "line_hsn_code": ("line_hsn_code", ["HS Code", "Tariff Code", "HSN"]),
    "line_isic_code": ("line_isic_code", ["ISIC Code", "Service Code", "Activity Code"]),
    "line_product_category": ("line_product_category", ["Product Category", "Item Category"]),
    "line_service_category": ("line_service_category", ["Service Category", "Service Type"]),
    "line_sellers_item_identification": ("line_sellers_item_identification",
                                         ["SKU", "Item Code", "Product Code", "Seller Item ID"]),
    "line_price_unit": ("line_price_unit", ["Unit", "UoM", "Unit of Measure"]),
    "line_tax_percent": ("vat_rate", ["Tax Rate", "Tax %", "Line Tax %"]),
    "line_base_quantity": ("line_base_quantity", ["Base Quantity", "Pricing Quantity", "Price Per Qty"]),
}
# Per BUYERS entry: (state, LGA, city, postal zone, invoice kind). Per ITEMS entry: (HS, ISIC, price unit, tax category,
# product category, service category).
BUYER_NRS = [("NG-LA", "NG-LA-IKE", "Lagos", "100001", "B2B"), ("NG-KN", "NG-KN-NAS", "Kano", "700001", "B2B"),
             ("NG-DE", "NG-DE-WSO", "Warri", "332001", "B2B"), ("NG-FC", "NG-FC-AML", "Abuja", "900001", "B2G"),
             ("NG-EN", "NG-EN-ENO", "Enugu", "400001", "B2B"), ("NG-OY", "NG-OY-INO", "Ibadan", "200001", "B2C"),
             ("NG-RI", "NG-RI-PHA", "Port Harcourt", "500001", "B2B"), ("NG-PL", "NG-PL-JNO", "Jos", "930001", "B2C"),
             ("NG-CR", "NG-CR-CMU", "Calabar", "540001", "B2C"), ("NG-IM", "NG-IM-OMU", "Owerri", "460001", "B2B"),
             ("NG-KD", "NG-KD-KNO", "Kaduna", "800001", "B2G"), ("NG-ED", "NG-ED-ORE", "Benin City", "300001", "B2B")]
ITEM_NRS = [("0101.21", "0111", "LTR", "STANDARD_GST", "Fuel", "Delivery"),
            ("0101.29", "0112", "KGM", "STANDARD_GST", "Building materials", "Supply"),
            ("0101.30", "0113", "HUR", "ZERO_GST", "Consulting", "Advisory"),
            ("0101.21", "0111", "C62", "STANDARD_GST", "Office supplies", "Supply"),
            ("0101.29", "0112", "MTR", "REDUCED_GST", "Freight", "Haulage"),
            ("0101.30", "0113", "C62", "STANDARD_GST", "Electronics", "Repair"),
            ("0101.21", "0111", "MON", "ZERO_GST", "Property", "Rental"),
            ("0101.29", "0112", "MON", "STANDARD_GST", "Security", "Guarding"),
            ("0101.30", "0113", "C62", "REDUCED_GST", "Beverages", "Supply"),
            ("0101.21", "0111", "C62", "STANDARD_GST", "Energy equipment", "Supply")]
MON = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
NUMBERING = ["INV-{:05d}", "{:06d}", "SI/2026/{:04d}", "{}", "INV{}", "BILL-2026-{:04d}"]


def norm(s):
    return re.sub(r"[^a-z0-9]", "", s.lower())


def money(x, style):
    if style == "plain":
        return f"{x:.2f}"
    s = f"{x:,.2f}"
    if style == "grouped":
        return s
    if style == "naira":
        return "₦" + s
    return s.replace(",", "_").replace(".", ",").replace("_", ".")  # euro


def date_s(d, style):
    return {"YYYY-MM-DD": d.isoformat(), "DD/MM/YYYY": f"{d.day:02d}/{d.month:02d}/{d.year}",
            "MM/DD/YYYY": f"{d.month:02d}/{d.day:02d}/{d.year}",
            "DD-MMM-YYYY": f"{d.day:02d}-{MON[d.month - 1]}-{d.year}"}[style]


def styled(header, style):
    if style == "upper":
        return header.upper()
    if style == "snake":
        return re.sub(r"[^a-z0-9]+", "_", header.lower()).strip("_")
    return header


def invoices(rng, ctx, has_lines, single, want_rows):
    customers = []
    for name in rng.sample(BUYERS, 6):
        tin = f"{rng.randint(10000000, 99999999)}-{rng.randint(1, 9999):04d}"
        customers.append({"buyer": name, "tin": tin.replace("-", "") if ctx["tin_plain"] else tin,
                          "cust_id": "CUS-" + f"{rng.randint(1, 999):04d}", "cust_num": str(rng.randint(100000, 199999)),
                          "email": "accounts@" + norm(name)[:12] + ".ng", "phone": "080" + str(rng.randint(10000000, 99999999)),
                          "address": f"{rng.randint(1, 99)} Marina Road, Lagos"})
    n = rng.randint(100, 9000)
    out, rows = [], 0
    while rows < want_rows:
        month = rng.randint(1, 8)
        day = rng.randint(1, 12) if ctx["ambiguous_dates"] else (rng.randint(13, 28) if not out else rng.randint(1, 28))
        lines = []
        for _ in range(1 if (single or not has_lines) else rng.randint(1, 3)):
            desc, desc2 = rng.choice(ITEMS)
            lines.append({"desc": desc, "desc2": desc2, "qty": rng.randint(1, 40),
                          "price": round(rng.uniform(500, 250000), 2)})
        sub = round(sum(l["qty"] * l["price"] for l in lines), 2)
        vat = round(sub * 0.075, 2)
        inv = dict(rng.choice(customers))
        inv.update({"number": ctx["numbering"].format(n), "date": dt.date(2026, month, day), "time": f"{rng.randint(8, 19):02d}:{rng.randint(0, 59):02d}",
                    "lines": lines, "subtotal": sub, "vat": vat, "total": round(sub + vat, 2),
                    "wht": round(sub * 0.05, 2), "paid": round((sub + vat) * rng.choice([0, 0.5, 1]), 2),
                    "status": rng.choice(["Paid", "Sent", "Overdue", "Draft"]), "po": "PO-" + str(rng.randint(1000, 9999)),
                    "desk": "Desk " + str(rng.randint(1, 4)), "branch": rng.choice(["Ikeja", "Wuse", "Trans-Amadi"]),
                    "memo": "Supply for " + MON[month - 1], "internal": str(rng.randint(4839000000000, 4839999999999)),
                    "pay": rng.choice(["Cash", "Card", "Transfer"])})
        n += rng.randint(1, 3)
        out.append(inv)
        rows += len(lines)
    return out


def cell(kind, inv, line, ctx, serial):
    m = lambda x: money(x, ctx["money"])
    d = lambda x: date_s(x, ctx["date"])
    bn = lambda: BUYER_NRS[BUYERS.index(inv["buyer"])]
    itn = lambda: ITEM_NRS[ITEMS.index((line["desc"], line["desc2"]))]
    value = {
        "inv_no": lambda: inv["number"], "date": lambda: d(inv["date"]), "datetime": lambda: d(inv["date"]) + " " + inv["time"],
        "due_date": lambda: d(inv["date"] + dt.timedelta(days=30)), "buyer_tin": lambda: inv["tin"],
        "buyer_name": lambda: inv["buyer"], "currency": lambda: "NGN", "subtotal": lambda: m(inv["subtotal"]),
        "vat": lambda: m(inv["vat"]), "total": lambda: m(inv["total"]), "line_desc": lambda: line["desc"],
        "line_desc2": lambda: line["desc2"], "qty": lambda: str(line["qty"]), "unit_price": lambda: m(line["price"]),
        "line_total": lambda: m(line["qty"] * line["price"]), "line_tax": lambda: m(round(line["qty"] * line["price"] * 0.075, 2)),
        "seller_tin": lambda: ctx["seller_tin"], "rc_no": lambda: ctx["rc"], "po_no": lambda: inv["po"],
        "customer_id": lambda: inv["cust_id"], "customer_num": lambda: inv["cust_num"], "email": lambda: inv["email"],
        "phone": lambda: inv["phone"], "address": lambda: inv["address"], "status": lambda: inv["status"],
        "discount": lambda: m(0), "wht": lambda: m(inv["wht"]), "net_payable": lambda: m(inv["total"] - inv["wht"]),
        "vat_rate": lambda: "7,5%" if ctx["money"] == "euro" else "7.5%", "paid": lambda: m(inv["paid"]),
        "balance": lambda: m(inv["total"] - inv["paid"]), "exchange_rate": lambda: "1,00" if ctx["money"] == "euro" else "1.00",
        "salesperson": lambda: inv["desk"], "branch": lambda: inv["branch"], "memo": lambda: inv["memo"],
        "terms": lambda: "Net 30", "type": lambda: "Invoice", "type_si": lambda: "SI", "voucher_type": lambda: "Sales",
        "tax_code": lambda: "VAT7.5", "internal_id": lambda: inv["internal"], "serial": lambda: str(serial),
        "pay_method": lambda: inv["pay"], "pay_status": lambda: inv["status"],
        "invoice_kind": lambda: bn()[4], "tax_currency_code": lambda: "NGN", "issue_time": lambda: inv["time"] + ":00",
        "tax_point_date": lambda: d(inv["date"]), "buyer_city": lambda: bn()[2], "buyer_postal_zone": lambda: bn()[3],
        "buyer_country": lambda: "NG", "buyer_state": lambda: bn()[0], "buyer_lga": lambda: bn()[1],
        "line_hsn_code": lambda: itn()[0], "line_isic_code": lambda: itn()[1], "line_price_unit": lambda: itn()[2],
        "line_tax_category": lambda: itn()[3], "line_product_category": lambda: itn()[4],
        "line_service_category": lambda: itn()[5], "line_base_quantity": lambda: "1",
        "line_sellers_item_identification": lambda: f"SKU-{ITEMS.index((line['desc'], line['desc2'])) + 1:03d}",
    }[kind]
    return value()


def build(rng, lid, category, columns, single, title_rows=()):
    ctx = {"money": rng.choices(["plain", "grouped", "naira", "euro"], [.3, .4, .15, .15])[0],
           "date": rng.choices(["YYYY-MM-DD", "DD/MM/YYYY", "MM/DD/YYYY", "DD-MMM-YYYY"], [.3, .4, .1, .2])[0],
           "numbering": rng.choice(NUMBERING), "tin_plain": rng.random() < .25,
           "seller_tin": f"{rng.randint(10000000, 99999999)}-0001", "rc": "RC " + str(rng.randint(100000, 999999))}
    ctx["ambiguous_dates"] = ctx["date"] in ("DD/MM/YYYY", "MM/DD/YYYY") and rng.random() < .5
    has_lines = any(k in LINE_KINDS for _, k in columns)
    header = [h for h, _ in columns]
    data, serial = [], 0
    for inv in invoices(rng, ctx, has_lines, single, 12):
        for line in inv["lines"]:
            serial += 1
            data.append([cell(k, inv, line, ctx, serial) for _, k in columns])
    key = {f: [h for h, k in columns if FIELD_OF.get(k) == f] or [None] for f in FIELDS}
    rows = [list(r) for r in title_rows] + [header] + data
    buf = io.StringIO()
    csv.writer(buf, lineterminator="\n").writerows(rows)
    os.makedirs(os.path.join(DATA, "csv"), exist_ok=True)
    with open(os.path.join(DATA, "csv", lid + ".csv"), "w", encoding="utf-8") as fh:
        fh.write(buf.getvalue())
    return {"id": lid, "category": category, "columns": header, "header_row": len(title_rows) + 1, "rows": rows,
            "key": key, "date_format": ctx["date"], "number_style": ctx["money"],
            "decimal_separator": "," if ctx["money"] == "euro" else ".", "ambiguous_dates": ctx["ambiguous_dates"],
            "single_line": single}


def composed_columns(rng):
    fields = ["invoice_number"] + [f for f, p in P_FIELD.items() if rng.random() < p]
    cols, seen = [], set()

    def add(h, k):
        if norm(h) not in seen:
            seen.add(norm(h))
            cols.append((h, k))

    for f in fields:
        add(rng.choice([s for s in SYN[f] if norm(s) not in seen] or SYN[f]), KIND_OF[f])
    if "issue_date" in fields and rng.random() < .5:
        add("Due Date", "due_date")
    if rng.random() < .35:
        add(rng.choice(["Supplier TIN", "Our TIN", "Seller TIN"]), "seller_tin")
    if "line_unit_price" in fields and rng.random() < .5:
        add(rng.choice(["Line Total", "Line Amount", "Amount"]), "line_total")
    if "vat" in fields and rng.random() < .4:
        add(rng.choice(["VAT Rate", "VAT %"]), "vat_rate")
    if "total" in fields and rng.random() < .4:
        h, k = rng.choice([("Balance Due", "balance"), ("Amount Paid", "paid"), ("Outstanding", "balance")])
        add(h, k)
    if rng.random() < .35:
        add(rng.choice(["PO Number", "Order No", "Customer Ref"]), "po_no")
    if "buyer_name" in fields and rng.random() < .35:
        add(rng.choice(["Customer Code", "Customer ID", "Account No"]), "customer_id")
    for h, k in rng.sample(FILLERS, rng.randint(0, 2)):
        add(h, k)
    inv = cols.pop(0)
    rng.shuffle(cols)
    cols.insert(rng.randint(0, 2) if rng.random() < .7 else rng.randint(0, len(cols)), inv)
    style = rng.choice(["asis", "asis", "asis", "upper", "snake"])
    out = [(styled(h, style), k) for h, k in cols]
    if len({norm(h) for h, _ in out}) < len(out) or any(h == "" for h, _ in out):
        out = cols
    return out


def nrs_columns(rng):
    cols = composed_columns(rng)
    seen = {norm(h) for h, _ in cols}
    have = {FIELD_OF.get(k) for _, k in cols}
    for f, (kind, headers) in NRS_SYN.items():
        if rng.random() >= .5 or f in have:
            continue
        free = [h for h in headers if norm(h) not in seen]
        if free:
            h = rng.choice(free)
            seen.add(norm(h))
            cols.insert(rng.randint(0, len(cols)), (h, kind))
    return cols


TITLES = [["Sales Invoice Register"], ["Example Trading Ltd - TIN 12345678-0001"],
          ["Period: 01/01/2026 to 31/08/2026"], ["Generated 2026-09-01 08:14"], []]


def main():
    rng = random.Random(SEED)
    layouts = []
    for name, single, cols in SOFTWARE:
        layouts.append(build(rng, "sw_" + name, "software", cols, single))
    for i in range(27):
        cols = composed_columns(rng)
        layouts.append(build(rng, f"mix_{i + 1:02d}", "composed", cols, False))
    for i in range(8):
        cols = composed_columns(rng)
        titles = rng.sample(TITLES[:4], rng.randint(1, 3)) + [[]]
        layouts.append(build(rng, f"title_{i + 1:02d}", "structure", cols, False, titles))
    rng2 = random.Random(SEED + 1)
    for i in range(24):
        layouts.append(build(rng2, f"nrs_{i + 1:02d}", "nrs", nrs_columns(rng2), False))
    with open(os.path.join(DATA, "layouts.json"), "w", encoding="utf-8") as fh:
        json.dump(layouts, fh, ensure_ascii=False, indent=1, default=str)
    print(f"{len(layouts)} layouts written to {DATA}")


if __name__ == "__main__":
    main()

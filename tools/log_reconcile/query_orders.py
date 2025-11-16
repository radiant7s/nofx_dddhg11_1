import sys
import sqlite3
import datetime as dt
import time as _time
from typing import Tuple

DB_PATH = r"d:\ai\nofx-dev\tools\log_reconcile\reconcile.db"

USAGE = """
Usage:
  python query_orders.py <trader_id> <symbol> <date: YYYY-MM-DD> [tz_offset_hours]

Examples:
  python query_orders.py binance_xxx_deepseek_xxx 0GUSDT 2025-11-09
  python query_orders.py binance_xxx_deepseek_xxx BTCUSDT 2025-11-09 8
"""

def day_range_ms(date_str: str, tz_hours: int = 8) -> Tuple[int, int]:
    y, m, d = map(int, date_str.split("-"))
    tz = dt.timezone(dt.timedelta(hours=tz_hours))
    start = int(dt.datetime(y, m, d, 0, 0, 0, tzinfo=tz).timestamp() * 1000)
    end = int(dt.datetime(y, m, d, 23, 59, 59, 999000, tzinfo=tz).timestamp() * 1000)
    return start, end


def main():
    if len(sys.argv) < 4:
        print(USAGE.strip())
        sys.exit(1)
    trader_id = sys.argv[1]
    symbol = sys.argv[2]
    date_str = sys.argv[3]
    tz_hours = int(sys.argv[4]) if len(sys.argv) > 4 else 8

    start, end = day_range_ms(date_str, tz_hours)

    con = sqlite3.connect(DB_PATH)
    cur = con.cursor()
    cur.execute(
        """
        SELECT order_id, side, position_side, status, avg_price, executed_qty,
               reduce_only, close_position, type, time
        FROM orders
        WHERE trader_id=? AND symbol=? AND time BETWEEN ? AND ?
        ORDER BY time
        """,
        (trader_id, symbol, start, end),
    )
    rows = cur.fetchall()
    print(f"found {len(rows)} orders for {symbol} on {date_str} (tz=UTC{tz_hours:+d})")
    for (order_id, side, pos_side, status, avg_price, executed_qty, reduce_only, close_position, otype, ts) in rows:
        ts_str = _time.strftime("%Y-%m-%d %H:%M:%S", _time.localtime(ts / 1000))
        print(
            f"{ts_str} | id={order_id} | {side:<4} {pos_side:<5} {status:<7} | px={avg_price:.6g} qty={executed_qty:.6g} | RO={reduce_only} CP={close_position} | {otype}"
        )

    con.close()


if __name__ == "__main__":
    main()

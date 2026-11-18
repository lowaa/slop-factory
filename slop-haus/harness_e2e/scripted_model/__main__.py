import argparse
import logging

from .server import make_server


def main() -> None:
    parser = argparse.ArgumentParser(description="A scripted model server for harness tests.")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
    make_server(args.host, args.port).serve_forever()


main()

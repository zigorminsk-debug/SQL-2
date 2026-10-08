import io
import unittest
from console import configure_console


class ConsoleTests(unittest.TestCase):
    def test_redirected_western_encoding_accepts_russian(self):
        buffer = io.BytesIO()
        stream = io.TextIOWrapper(buffer, encoding="cp1252")
        configure_console([stream])
        print("Демо-отчёт готов", file=stream, flush=True)
        self.assertEqual(buffer.getvalue().decode("utf-8"), "Демо-отчёт готов\n")

    def test_windowed_executable_without_console(self):
        configure_console([None, None])


if __name__ == '__main__':
    unittest.main()

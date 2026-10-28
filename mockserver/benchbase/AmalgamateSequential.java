import com.oltpbenchmark.benchmarks.smallbank.procedures.Amalgamate;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.Arrays;

// Sequential integration check. Use only a fresh, dedicated SmallBank database.
// Invokes the production Java entry point, not a reimplementation of its SQL.
public final class AmalgamateSequential {
  private static double[] balances(Connection conn) throws Exception {
    double[] values = new double[4];
    try (Statement stmt = conn.createStatement();
        ResultSet rows = stmt.executeQuery(
            "SELECT custid, bal FROM savings ORDER BY custid")) {
      for (int i = 0; i < 2; i++) {
        if (!rows.next() || rows.getLong(1) != i) throw new AssertionError("savings fixture");
        values[i] = rows.getDouble(2);
      }
      if (rows.next()) throw new AssertionError("extra savings");
    }
    try (Statement stmt = conn.createStatement();
        ResultSet rows = stmt.executeQuery(
            "SELECT custid, bal FROM checking ORDER BY custid")) {
      for (int i = 0; i < 2; i++) {
        if (!rows.next() || rows.getLong(1) != i) throw new AssertionError("checking fixture");
        values[i + 2] = rows.getDouble(2);
      }
      if (rows.next()) throw new AssertionError("extra checking");
    }
    return values;
  }

  private static void expect(Connection conn, String stage, double[] expected) throws Exception {
    double[] actual = balances(conn);
    System.out.println(stage + " [s0,s1,c0,c1]=" + Arrays.toString(actual)
        + " total=" + Arrays.stream(actual).sum());
    if (!Arrays.equals(actual, expected)) throw new AssertionError(stage);
  }

  public static void main(String[] args) throws Exception {
    if (args.length != 4 || !(args[3].equals("original") || args[3].equals("fixed"))) {
      throw new IllegalArgumentException("JDBC_URL USER PASSWORD original|fixed; fresh database only");
    }
    boolean fixed = args[3].equals("fixed");
    try (Connection conn = DriverManager.getConnection(args[0], args[1], args[2])) {
      // No DROP/DELETE: accidental reuse of an existing database fails at CREATE TABLE.
      try (Statement stmt = conn.createStatement()) {
        stmt.executeUpdate("CREATE TABLE accounts (custid BIGINT PRIMARY KEY, name VARCHAR(64))");
        stmt.executeUpdate("CREATE TABLE savings (custid BIGINT PRIMARY KEY, bal DOUBLE)");
        stmt.executeUpdate("CREATE TABLE checking (custid BIGINT PRIMARY KEY, bal DOUBLE)");
        stmt.executeUpdate("INSERT INTO accounts VALUES (0, 'zero'), (1, 'one')");
        stmt.executeUpdate("INSERT INTO savings VALUES (0, 2500), (1, 2500)");
        stmt.executeUpdate("INSERT INTO checking VALUES (0, 2500), (1, 2500)");
        stmt.execute("CALL dbcop_seal_initial_state()");
        stmt.execute("SET @@session.dbcop_read_latest = 1");
      }
      conn.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);
      conn.setAutoCommit(false);
      System.out.println("procedure=" + Amalgamate.class.getProtectionDomain().getCodeSource().getLocation());
      expect(conn, "before", new double[] {2500, 2500, 2500, 2500});
      Amalgamate procedure = new Amalgamate();
      procedure.run(conn, 0, 1);
      conn.commit();
      expect(conn, "after 0->1", fixed
          ? new double[] {0, 2500, 0, 7500}
          : new double[] {2500, -2500, 0, 2500});
      conn.commit();
      procedure.run(conn, 1, 0);
      conn.commit();
      expect(conn, "after 1->0", fixed
          ? new double[] {0, 0, 10000, 0}
          : new double[] {5000, -2500, 0, 0});
      conn.commit();
    }
  }
}

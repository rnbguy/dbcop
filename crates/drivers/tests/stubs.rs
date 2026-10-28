use dbcop_drivers::antidotedb::AntidoteDbDriver;
use dbcop_drivers::cockroachdb::CockroachDbDriver;
use dbcop_drivers::galera::GaleraDriver;
use dbcop_drivers::{ClusterConfig, DbDriver};

fn test_config() -> ClusterConfig {
    ClusterConfig {
        hosts: vec!["127.0.0.1".to_string()],
        port: 1,
        db_name: "test".to_string(),
    }
}

#[test]
#[should_panic(expected = "not yet implemented")]
fn galera_connect_is_unimplemented_stub() {
    let _ = GaleraDriver::connect(&test_config());
}

#[test]
#[should_panic(expected = "not yet implemented")]
fn antidotedb_connect_is_unimplemented_stub() {
    let _ = AntidoteDbDriver::connect(&test_config());
}

#[test]
#[should_panic(expected = "not yet implemented")]
fn cockroachdb_connect_is_unimplemented_stub() {
    let _ = CockroachDbDriver::connect(&test_config());
}

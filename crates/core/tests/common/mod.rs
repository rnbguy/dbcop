/// DSL macro for building test histories.
///
/// Produces `Vec<Session<&'static str, u64>>`.
///
/// # Syntax
///
/// ```ignore
/// history! {
///     [
///         { w(x, 1), w(y, 1) },          // committed transaction
///         uncommitted { w(z, 99) },        // uncommitted transaction
///     ],
///     [
///         { r(x, 1), r(y, 1) },
///         { r(z) },                        // r(var) → read_empty
///     ],
/// }
/// ```
///
/// - `w(var, val)` → `Event::write("var", val)`
/// - `r(var, val)` → `Event::read("var", val)`
/// - `r(var)`      → `Event::read_empty("var")`
///
/// Build a single Event.
#[macro_export]
macro_rules! ev {
    (w($var:ident, $val:expr)) => {
        dbcop_core::history::raw::types::Event::<&'static str, u64>::write(
            stringify!($var),
            $val as u64,
        )
    };
    (r($var:ident, $val:expr)) => {
        dbcop_core::history::raw::types::Event::<&'static str, u64>::read(
            stringify!($var),
            $val as u64,
        )
    };
    (r($var:ident)) => {
        dbcop_core::history::raw::types::Event::<&'static str, u64>::read_empty(stringify!($var))
    };
}

/// Build a committed Transaction.
#[macro_export]
macro_rules! txn_committed {
    ($($e:ident($($args:tt)*)),* $(,)?) => {
        dbcop_core::history::raw::types::Transaction::<&'static str, u64>::committed(
            vec![$($crate::ev!($e($($args)*))),*]
        )
    };
}

/// Build an uncommitted Transaction.
#[macro_export]
macro_rules! txn_uncommitted {
    ($($e:ident($($args:tt)*)),* $(,)?) => {
        dbcop_core::history::raw::types::Transaction::<&'static str, u64>::uncommitted(
            vec![$($crate::ev!($e($($args)*))),*]
        )
    };
}

/// Internal TT-muncher: accumulate transactions into a session Vec.
#[macro_export]
macro_rules! session_inner {
    // Base: nothing left
    (@acc $acc:expr ;) => { $acc };

    // uncommitted { ... } , rest
    (@acc $acc:expr ; uncommitted { $($e:ident($($args:tt)*)),* $(,)? } , $($rest:tt)*) => {{
        let mut v = $acc;
        v.push($crate::txn_uncommitted!($($e($($args)*)),*));
        $crate::session_inner!(@acc v ; $($rest)*)
    }};

    // uncommitted { ... }  trailing (no comma)
    (@acc $acc:expr ; uncommitted { $($e:ident($($args:tt)*)),* $(,)? }) => {{
        let mut v = $acc;
        v.push($crate::txn_uncommitted!($($e($($args)*)),*));
        v
    }};

    // { ... } , rest
    (@acc $acc:expr ; { $($e:ident($($args:tt)*)),* $(,)? } , $($rest:tt)*) => {{
        let mut v = $acc;
        v.push($crate::txn_committed!($($e($($args)*)),*));
        $crate::session_inner!(@acc v ; $($rest)*)
    }};

    // { ... }  trailing (no comma)
    (@acc $acc:expr ; { $($e:ident($($args:tt)*)),* $(,)? }) => {{
        let mut v = $acc;
        v.push($crate::txn_committed!($($e($($args)*)),*));
        v
    }};
}

/// Build one Session from transaction blocks.
#[macro_export]
macro_rules! session {
    () => {
        Vec::<dbcop_core::history::raw::types::Transaction::<&'static str, u64>>::new()
    };
    ($($rest:tt)*) => {{
        let v = Vec::<dbcop_core::history::raw::types::Transaction::<&'static str, u64>>::new();
        $crate::session_inner!(@acc v ; $($rest)*)
    }};
}

/// Build a full history: sessions are `[ ... ]` blocks.
#[macro_export]
macro_rules! history {
    ($( [ $($txns:tt)* ] ),* $(,)?) => {
        vec![
            $($crate::session!($($txns)*)),*
        ]
    };
}

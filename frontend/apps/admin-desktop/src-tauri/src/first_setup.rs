use serde::{Deserialize, Serialize};
use serde_json::Value;
use zeroize::Zeroize;

use crate::proxy::PublicProfile;

pub(crate) const STATE_ACTION: &str = "first-setup-state";
pub(crate) const SUBMIT_ACTION: &str = "first-setup-submit";

#[cfg(not(feature = "native-e2e"))]
pub(crate) const PATH: &str = "/__desktop/first-setup";
#[cfg(feature = "native-e2e")]
pub(crate) const PATH: &str = "/__desktop/test-control";

#[derive(Deserialize, Zeroize)]
#[zeroize(drop)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct FirstSetupInput {
    pub(crate) username: String,
    pub(crate) display_name: String,
    pub(crate) email: String,
    pub(crate) password: String,
}

impl FirstSetupInput {
    pub(crate) fn valid(&self) -> bool {
        let username = self.username.trim();
        let display_name = self.display_name.trim();
        let email = self.email.trim();
        (3..=64).contains(&username.len())
            && (1..=80).contains(&display_name.chars().count())
            && (3..=254).contains(&email.len())
            && email.contains('@')
            && !email.chars().any(char::is_whitespace)
            && (12..=128).contains(&self.password.len())
            && !self.password.contains('\0')
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
enum StateName {
    Required,
    LoginRequired,
    Unavailable,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct StateWire {
    state: StateName,
}

#[derive(Serialize)]
#[serde(rename_all = "kebab-case", tag = "state")]
pub(crate) enum FirstSetupState {
    Required,
    LoginRequired,
    Unavailable,
}

impl From<StateName> for FirstSetupState {
    fn from(value: StateName) -> Self {
        match value {
            StateName::Required => Self::Required,
            StateName::LoginRequired => Self::LoginRequired,
            StateName::Unavailable => Self::Unavailable,
        }
    }
}

#[derive(Serialize)]
#[serde(rename_all = "kebab-case", tag = "state")]
pub(crate) enum FirstSetupOutcome {
    Complete { profile: PublicProfile },
    LoginRequired,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct CompleteWire {
    #[serde(rename = "state")]
    _state: CompleteState,
    profile: PublicProfile,
    session_token: String,
    csrf_token: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
enum CompleteState {
    Complete,
}

pub(crate) struct SetupSession {
    pub(crate) profile: PublicProfile,
    pub(crate) token: String,
    pub(crate) csrf: String,
}

impl Drop for SetupSession {
    fn drop(&mut self) {
        self.token.zeroize();
        self.csrf.zeroize();
    }
}

pub(crate) fn decode_state(value: Value) -> Result<FirstSetupState, &'static str> {
    serde_json::from_value::<StateWire>(value)
        .map(|wire| wire.state.into())
        .map_err(|_| "desktop first setup state invalid")
}

pub(crate) fn decode_complete(value: Value) -> Result<SetupSession, &'static str> {
    let wire: CompleteWire =
        serde_json::from_value(value).map_err(|_| "desktop first setup response invalid")?;
    Ok(SetupSession {
        profile: wire.profile,
        token: wire.session_token,
        csrf: wire.csrf_token,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn setup_input_and_state_wire_are_strict() {
        assert!(
            FirstSetupInput {
                username: "admin".into(),
                display_name: "Administrator".into(),
                email: "admin@example.test".into(),
                password: "correct horse battery staple".into(),
            }
            .valid()
        );
        assert!(decode_state(serde_json::json!({"state":"required"})).is_ok());
        assert!(decode_state(serde_json::json!({"state":"required","extra":true})).is_err());
        assert!(decode_complete(serde_json::json!({
            "state":"complete",
            "profile":{"id":"account-1","username":"admin","displayName":"Admin","email":"admin@example.test","avatarRef":null},
            "sessionToken":"abcdefghijklmnopqrstuvwxyzABCDEFGH123456789",
            "csrfToken":"987654321HGFEDCBAzyxwvutsrqponmlkjihgfedcba"
        })).is_ok());
    }
}

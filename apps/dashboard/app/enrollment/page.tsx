import { requireChatGPTUser } from "../chatgpt-auth";
import { siteEnrollmentForUser } from "../site-enrollment";

export const dynamic = "force-dynamic";

export default async function EnrollmentPage() {
  const user = await requireChatGPTUser("/enrollment");
  const enrollment = await siteEnrollmentForUser(user);

  return (
    <main style={{ minHeight: "100vh", padding: "clamp(2rem, 8vw, 7rem)", background: "#050607", color: "#f4f5f0" }}>
      <section style={{ width: "min(100%, 760px)", margin: "0 auto" }}>
        <p style={{ color: "#b9ff42", letterSpacing: "0.14em", textTransform: "uppercase" }}>Owner bootstrap</p>
        <h1 style={{ fontSize: "clamp(2.4rem, 7vw, 5.5rem)", lineHeight: 0.96, letterSpacing: "-0.06em" }}>
          Sites enrollment identity
        </h1>
        <p style={{ maxWidth: "58ch", color: "#a7aaa2", lineHeight: 1.7 }}>
          Use these values only with the audited FlowOps owner bootstrap command. They grant no control-plane access by themselves.
        </p>
        {enrollment ? (
          <dl style={{ marginTop: "3rem", borderTop: "1px solid #2b2e29" }}>
            <EnrollmentField label="Project" value={enrollment.siteProjectId} />
            <EnrollmentField label="Email" value={enrollment.email} />
            <EnrollmentField label="Site user key" value={enrollment.siteUserKey} />
          </dl>
        ) : (
          <p role="status" style={{ marginTop: "3rem", color: "#ffb84d" }}>
            Enrollment is not configured for this deployment.
          </p>
        )}
      </section>
    </main>
  );
}

function EnrollmentField({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "grid", gap: "0.5rem", padding: "1.35rem 0", borderBottom: "1px solid #2b2e29" }}>
      <dt style={{ color: "#777b72", fontSize: "0.8rem", letterSpacing: "0.12em", textTransform: "uppercase" }}>{label}</dt>
      <dd style={{ margin: 0, overflowWrap: "anywhere", fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace" }}>{value}</dd>
    </div>
  );
}

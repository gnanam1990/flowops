import { requireChatGPTUser } from "../chatgpt-auth";
import { siteEnrollmentForUser } from "../site-enrollment";

export const dynamic = "force-dynamic";

export default async function EnrollmentPage() {
  const user = await requireChatGPTUser("/enrollment");
  const enrollment = await siteEnrollmentForUser(user);

  return (
    <main className="enrollment-page">
      <section className="enrollment-card">
        <div className="enrollment-brand"><span aria-hidden="true" />FlowOps</div>
        <p className="enrollment-eyebrow">Owner bootstrap</p>
        <h1>
          Sites enrollment identity
        </h1>
        <p className="enrollment-summary">
          Use these values only with the audited FlowOps owner bootstrap command. They grant no control-plane access by themselves.
        </p>
        {enrollment ? (
          <dl className="enrollment-fields">
            <EnrollmentField label="Project" value={enrollment.siteProjectId} />
            <EnrollmentField label="Email" value={enrollment.email} />
            <EnrollmentField label="Site user key" value={enrollment.siteUserKey} />
          </dl>
        ) : (
          <p className="enrollment-unavailable" role="status">
            Enrollment is not configured for this deployment.
          </p>
        )}
      </section>
    </main>
  );
}

function EnrollmentField({ label, value }: { label: string; value: string }) {
  return (
    <div className="enrollment-field">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

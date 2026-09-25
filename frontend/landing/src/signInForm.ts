// Pure validator for the landing sign-in form (AUTH-05 D7). Stub: final signatures only.

export type SignInFormValues = {
  email: string
  password: string
}

export type SignInFormErrors = {
  email?: string
  password?: string
}

export function validateSignInForm(_v: SignInFormValues): SignInFormErrors {
  return {}
}

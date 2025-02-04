import Doc from './doc'
import BasePage from './basepage'
import { postJSON } from './http'
import { NewWalletForm, UnlockWalletForm, DEXAddressForm, ConfirmRegistrationForm, bind as bindForm } from './forms'
import * as intl from './locales'

const DCR_ID = 42
const animationLength = 300

let app

export default class RegistrationPage extends BasePage {
  constructor (application, body) {
    super()
    app = application
    this.body = body
    this.notifiers = {}
    this.pwCache = {}
    const page = this.page = Doc.parsePage(body, [
      // Form 1: Set the application password
      'appPWForm', 'appPW', 'appPWAgain', 'appPWSubmit', 'appPWErrMsg',
      'showSeedRestore', 'seedRestore', 'seedInput', 'rememberPass',
      // Form 2: Create Decred wallet
      'newWalletForm',
      // Form 3: Unlock Decred wallet
      'unlockWalletForm',
      // Form 4: Configure DEX server
      'dexAddrForm', 'dexAddr',
      // Form 5: Confirm DEX registration and pay fee
      'confirmRegForm', 'failedRegForm', 'regFundsErr'
    ])

    // Hide the form closers for the registration process.
    body.querySelectorAll('.form-closer').forEach(el => Doc.hide(el))

    // SET APP PASSWORD
    bindForm(page.appPWForm, page.appPWSubmit, () => this.setAppPass())
    Doc.bind(page.showSeedRestore, 'click', () => {
      Doc.show(page.seedRestore)
      Doc.hide(page.showSeedRestore)
    })

    // NEW DCR WALLET
    // This form is only shown if there is no DCR wallet yet.
    this.walletForm = new NewWalletForm(app, page.newWalletForm, () => {
      this.dexAddrForm.refresh()
      this.changeForm(page.newWalletForm, page.dexAddrForm)
    }, this.pwCache)

    // OPEN DCR WALLET
    // This form is only shown if there is a wallet, but it's not open.
    this.unlockForm = new UnlockWalletForm(app, page.unlockWalletForm, () => {
      this.dexAddrForm.refresh()
      this.changeForm(page.unlockWalletForm, page.dexAddrForm)
    }, this.pwCache)

    // ADD DEX
    this.dexAddrForm = new DEXAddressForm(app, page.dexAddrForm, async (xc) => {
      const fee = xc.feeAsset.amount // DCR, special
      const balanceFeeRegistration = app.user.assets[DCR_ID].wallet.balance.available
      if (balanceFeeRegistration < fee) {
        const errorMsg = `Looks like there is not enough funds for
        paying the registration fee. Amount needed:
        ${Doc.formatCoinValue(fee / 1e8)} Amount available:
        ${Doc.formatCoinValue(balanceFeeRegistration / 1e8)}.

        Deposit funds and try again.`
        page.regFundsErr.textContent = errorMsg
        Doc.show(page.regFundsErr)
        await this.changeForm(page.dexAddrForm, page.failedRegForm)
        return
      }
      this.confirmRegisterForm.setExchange(xc)
      await this.changeForm(page.dexAddrForm, page.confirmRegForm)
    }, this.pwCache)

    // SUBMIT DEX REGISTRATION
    this.confirmRegisterForm = new ConfirmRegistrationForm(app,
      page.confirmRegForm,
      {
        getCertFile: () => this.getCertFile(),
        getDexAddr: () => this.getDexAddr()
      },
      () => this.registerDEXSuccess(),
      (msg) => this.registerDEXFundsFail(msg))
    // Attempt to load the dcrwallet configuration from the default location.
    if (app.user.authed) this.auth()
  }

  unload () {
    delete this.pwCache.pw
  }

  // auth should be called once user is known to be authed with the server.
  async auth () {
    await app.fetchUser()
    this.walletForm.setAsset(app.assets[DCR_ID])
    this.unlockForm.setAsset(app.assets[DCR_ID])
    this.walletForm.loadDefaults()
  }

  /* Swap this currently displayed form1 for form2 with an animation. */
  async changeForm (form1, form2) {
    const shift = this.body.offsetWidth / 2
    await Doc.animate(animationLength, progress => {
      form1.style.right = `${progress * shift}px`
    }, 'easeInHard')
    Doc.hide(form1)
    form1.style.right = '0'
    form2.style.right = -shift
    Doc.show(form2)
    if (form2.querySelector('input')) {
      form2.querySelector('input').focus()
    }
    await Doc.animate(animationLength, progress => {
      form2.style.right = `${-shift + progress * shift}px`
    }, 'easeOutHard')
    form2.style.right = '0'
  }

  /* Set the application password. Attached to form submission. */
  async setAppPass () {
    const page = this.page
    Doc.hide(page.appPWErrMsg)
    const pw = page.appPW.value
    const pwAgain = page.appPWAgain.value
    if (pw === '') {
      page.appPWErrMsg.textContent = intl.prep(intl.ID_NO_PASS_ERROR_MSG)
      Doc.show(page.appPWErrMsg)
      return
    }
    if (pw !== pwAgain) {
      page.appPWErrMsg.textContent = intl.prep(intl.ID_PASSWORD_NOT_MATCH)
      Doc.show(page.appPWErrMsg)
      return
    }

    // Clear the notification cache. Useful for development purposes, since
    // the Application will only clear them on login, which would leave old
    // browser-cached notifications in place after registering even if the
    // client db is wiped.
    app.setNotes([])
    page.appPW.value = ''
    page.appPWAgain.value = ''
    const loaded = app.loading(page.appPWForm)
    const seed = page.seedInput.value
    const rememberPass = page.rememberPass.checked
    const res = await postJSON('/api/init', {
      pass: pw,
      seed,
      rememberPass
    })
    loaded()
    if (!app.checkResponse(res)) {
      page.appPWErrMsg.textContent = res.msg
      Doc.show(page.appPWErrMsg)
      return
    }
    if (seed) this.walletForm.setRegMsg('Your Decred wallet is required')
    this.pwCache.pw = pw
    this.auth()
    app.updateMenuItemsDisplay()
    this.walletForm.refresh()
    await this.changeForm(page.appPWForm, page.newWalletForm)
  }

  /* gets the contents of the cert file */
  async getCertFile () {
    let cert = ''
    if (this.dexAddrForm.page.certFile.value) {
      cert = await this.dexAddrForm.page.certFile.files[0].text()
    }
    return cert
  }

  /* gets the dex address input by the user */
  getDexAddr () {
    return this.page.dexAddr.value
  }

  /* Called when dex registration failed due to insufficient funds. */
  async registerDEXFundsFail (msg) {
    const page = this.page
    page.regFundsErr.textContent = msg
    Doc.show(page.regFundsErr)
    await this.changeForm(page.confirmRegForm, page.failedRegForm)
  }

  /* Called after successful registration to a DEX. */
  async registerDEXSuccess () {
    await app.fetchUser()
    app.loadPage('markets')
  }
}

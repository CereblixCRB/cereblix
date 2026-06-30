Drop the gomobile-generated binding here as wallet.aar:

    cd C:\Users\Lisa\Desktop\Cereblix\staging\android
    gomobile bind -target=android -androidapi 21 -o android-app/app/libs/wallet.aar ./mobile

The AAR exposes the Java class `mobile.Mobile` with the static methods:
    Mobile.newAddress()                              -> String (JSON)
    Mobile.addressFromPriv(String)                   -> String
    Mobile.validateAddress(String)                   -> boolean
    Mobile.signSend(String,String,long,long,long,long) -> String (JSON)
    Mobile.coinUnit()                                -> long

See ../../BUILD.md for the full build steps.
